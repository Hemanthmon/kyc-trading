package auth

import (
	"encoding/json"
	"errors"
	"kyc-backend/models"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"
)

// Routes registers all auth/KYC endpoints on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", s.register)
	mux.HandleFunc("POST /auth/login", s.login)

	mux.Handle("GET /me", s.RequireAuth(http.HandlerFunc(s.me)))
	mux.Handle("POST /kyc/submit", s.RequireAuth(http.HandlerFunc(s.submitKYC)))

	admin := func(h http.HandlerFunc) http.Handler {
		return s.RequireAuth(RequireRole(models.RoleAdmin)(h))
	}
	mux.Handle("GET /admin/kyc/pending", admin(s.pendingKYC))
	mux.Handle("POST /admin/kyc/review", admin(s.reviewKYC))
	mux.Handle("POST /admin/users/role", admin(s.setRole))
}

// ---------- register / login ----------

// register: POST /auth/register {"email","password"} → new user with KYC "pending".
func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	
	var req models.LoginRequest
	if !readJSON(w, r, &req) {
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeError(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 { // bcrypt only uses the first 72 bytes
		writeError(w, http.StatusBadRequest, "password must be 8-72 characters")
		return
	}

	u, err := s.createUser(r.Context(), req.Email, req.Password, models.RoleUser, models.KYCPending)
	if errors.Is(err, errEmailTaken) {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create account")
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// dummyHash is compared against when the email doesn't exist, so a wrong email
// takes as long as a wrong password and attackers can't tell which emails exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.DefaultCost)

// login: POST /auth/login {"email","password"} → {"token": "..."}.
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var req models.LoginRequest
	if !readJSON(w, r, &req) {
		return
	}

	u, err := s.userByEmail(r.Context(), req.Email)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		writeError(w, http.StatusUnauthorized, "invalid email or password") // same message either way
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := s.newToken(u.ID.Hex())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_in": int(tokenTTL.Seconds()),
		"user":       u,
	})
}

// me: GET /me → the logged-in user (no password hash, document number masked).
func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, UserFrom(r.Context()))
}

// ---------- KYC ----------

var docNumberRe = regexp.MustCompile(`^[A-Za-z0-9]{5,30}$`)
var docTypes = []string{"passport", "national_id", "driving_license"}

type kycRequest struct {
	FullName       string `json:"full_name"`
	DocumentType   string `json:"document_type"`
	DocumentNumber string `json:"document_number"`
}

// submitKYC: POST /kyc/submit → status "submitted". Allowed only from "pending" or "rejected".
func (s *Service) submitKYC(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())

	var req kycRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	if len(req.FullName) < 2 || len(req.FullName) > 100 {
		writeError(w, http.StatusBadRequest, "full_name must be 2-100 characters")
		return
	}
	if !slices.Contains(docTypes, req.DocumentType) {
		writeError(w, http.StatusBadRequest, "document_type must be passport, national_id or driving_license")
		return
	}
	if !docNumberRe.MatchString(req.DocumentNumber) {
		writeError(w, http.StatusBadRequest, "document_number must be 5-30 letters/digits")
		return
	}

	enc, err := s.cipher.Encrypt(req.DocumentNumber) // never store the raw number
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not secure document")
		return
	}

	// The status condition in the filter makes the state change atomic:
	// two requests at the same time can't both succeed.
	res, err := s.users.UpdateOne(r.Context(),
		bson.M{"_id": u.ID, "kyc.status": bson.M{"$in": []string{models.KYCPending, models.KYCRejected}}},
		bson.M{"$set": bson.M{
			"kyc.status":              models.KYCSubmitted,
			"kyc.full_name":           req.FullName,
			"kyc.document_type":       req.DocumentType,
			"kyc.document_number_enc": enc,
			"kyc.document_last4":      req.DocumentNumber[len(req.DocumentNumber)-4:],
			"kyc.submitted_at":        time.Now().UTC(),
			"kyc.reject_reason":       "",
		}},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save KYC")
		return
	}
	if res.MatchedCount == 0 {
		writeError(w, http.StatusConflict, "KYC is already "+u.KYC.Status)
		return
	}

	s.logAudit(r.Context(), u.ID, "kyc_submitted", u.Email, "")
	writeJSON(w, http.StatusOK, map[string]string{"kyc_status": models.KYCSubmitted})
}

// pendingKYC: GET /admin/kyc/pending → users waiting for review.
func (s *Service) pendingKYC(w http.ResponseWriter, r *http.Request) {
	cur, err := s.users.Find(r.Context(), bson.M{"kyc.status": models.KYCSubmitted})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	users := []models.User{}
	if err := cur.All(r.Context(), &users); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type reviewRequest struct {
	UserID   string `json:"user_id"`
	Decision string `json:"decision"` // "approve" or "reject"
	Reason   string `json:"reason"`   // required when rejecting
}

// reviewKYC: POST /admin/kyc/review → "verified" or "rejected". Admin only.
func (s *Service) reviewKYC(w http.ResponseWriter, r *http.Request) {
	admin := UserFrom(r.Context())

	var req reviewRequest
	if !readJSON(w, r, &req) {
		return
	}
	target, err := bson.ObjectIDFromHex(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if target == admin.ID {
		writeError(w, http.StatusForbidden, "you cannot review your own KYC") // separation of duties
		return
	}

	set := bson.M{"kyc.reviewed_at": time.Now().UTC(), "kyc.reviewed_by": admin.Email}
	switch req.Decision {
	case "approve":
		set["kyc.status"] = models.KYCVerified
		set["kyc.reject_reason"] = ""
	case "reject":
		if strings.TrimSpace(req.Reason) == "" {
			writeError(w, http.StatusBadRequest, "reason is required when rejecting")
			return
		}
		set["kyc.status"] = models.KYCRejected
		set["kyc.reject_reason"] = req.Reason
	default:
		writeError(w, http.StatusBadRequest, `decision must be "approve" or "reject"`)
		return
	}

	// Only a "submitted" KYC can be reviewed.
	res, err := s.users.UpdateOne(r.Context(),
		bson.M{"_id": target, "kyc.status": models.KYCSubmitted},
		bson.M{"$set": set},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update KYC")
		return
	}
	if res.MatchedCount == 0 {
		writeError(w, http.StatusConflict, "user not found or KYC not in submitted state")
		return
	}

	s.logAudit(r.Context(), target, "kyc_"+req.Decision, admin.Email, req.Reason)
	writeJSON(w, http.StatusOK, map[string]string{"kyc_status": set["kyc.status"].(string)})
}

type roleRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// setRole: POST /admin/users/role → change a user's role. Admin only.
// A user can only become a merchant after KYC is verified.
func (s *Service) setRole(w http.ResponseWriter, r *http.Request) {
	admin := UserFrom(r.Context())

	var req roleRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !slices.Contains([]string{models.RoleUser, models.RoleMerchant, models.RoleAdmin}, req.Role) {
		writeError(w, http.StatusBadRequest, "role must be user, merchant or admin")
		return
	}
	target, err := bson.ObjectIDFromHex(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}

	filter := bson.M{"_id": target}
	if req.Role != models.RoleUser {
		filter["kyc.status"] = models.KYCVerified // merchants and admins must be verified
	}
	res, err := s.users.UpdateOne(r.Context(), filter, bson.M{"$set": bson.M{"role": req.Role}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update role")
		return
	}
	if res.MatchedCount == 0 {
		writeError(w, http.StatusConflict, "user not found or KYC not verified")
		return
	}

	s.logAudit(r.Context(), target, "role_"+req.Role, admin.Email, "")
	writeJSON(w, http.StatusOK, map[string]string{"role": req.Role})
}

// ---------- helpers ----------

// readJSON decodes a small JSON body; on failure it writes a 400 and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // max 1 MB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
