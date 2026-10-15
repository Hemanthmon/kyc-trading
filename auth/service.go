// Package auth handles accounts, login, KYC and access control.
package auth

import (
	"context"
	"errors"
	"fmt"
	"kyc-backend/models"
	"kyc-backend/secure"
	"log"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
)

const tokenTTL = time.Hour // a login token is valid for 1 hour

// Service holds everything auth needs: the collections, the JWT secret and the cipher.
type Service struct {
	users     *mongo.Collection
	audit     *mongo.Collection
	jwtSecret []byte
	cipher    *secure.Cipher
}

// NewService checks the secrets and returns a ready Service.
func NewService(db *mongo.Database, jwtSecret, encKeyHex string) (*Service, error) {
	if len(jwtSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 characters")
	}
	c, err := secure.NewCipher(encKeyHex)
	if err != nil {
		return nil, fmt.Errorf("KYC_ENC_KEY: %w", err)
	}
	return &Service{
		users:     db.Collection("users"),
		audit:     db.Collection("kyc_audit"),
		jwtSecret: []byte(jwtSecret),
		cipher:    c,
	}, nil
}

// Setup creates the unique email index and, if ADMIN_EMAIL/ADMIN_PASSWORD
// are given, makes sure that admin account exists (the first admin has to come from somewhere).
func (s *Service) Setup(ctx context.Context, adminEmail, adminPassword string) error {
	_, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true), // two accounts can never share an email
	})
	if err != nil {
		return fmt.Errorf("create email index: %w", err)
	}

	if adminEmail == "" || adminPassword == "" {
		return nil
	}
	_, err = s.createUser(ctx, adminEmail, adminPassword, models.RoleAdmin, models.KYCVerified)
	if errors.Is(err, errEmailTaken) {
		return nil // admin already exists
	}
	if err == nil {
		log.Println("admin account created:", adminEmail)
	}
	return err
}

var errEmailTaken = errors.New("email already registered")

// createUser hashes the password with bcrypt and inserts the user.
func (s *Service) createUser(ctx context.Context, email, password, role, kycStatus string) (*models.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &models.User{
		ID:           bson.NewObjectID(),
		Email:        strings.ToLower(strings.TrimSpace(email)),
		PasswordHash: string(hash),
		Role:         role,
		KYC:          models.KYC{Status: kycStatus},
		CreatedAt:    time.Now().UTC(),
	}
	if _, err := s.users.InsertOne(ctx, u); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, errEmailTaken
		}
		return nil, err
	}
	return u, nil
}

func (s *Service) userByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User
	err := s.users.FindOne(ctx, bson.M{"email": strings.ToLower(strings.TrimSpace(email))}).Decode(&u)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Service) userByID(ctx context.Context, id string) (*models.User, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}
	var u models.User
	if err := s.users.FindOne(ctx, bson.M{"_id": oid}).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

// logAudit records who changed a KYC status and when. Failures are logged, not fatal.
func (s *Service) logAudit(ctx context.Context, userID bson.ObjectID, action, by, note string) {
	_, err := s.audit.InsertOne(ctx, bson.M{
		"user_id": userID,
		"action":  action,
		"by":      by,
		"note":    note,
		"at":      time.Now().UTC(),
	})
	if err != nil {
		log.Println("audit log failed:", err)
	}
}
