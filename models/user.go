package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Roles: what a user is allowed to do.
const (
	RoleUser     = "user"
	RoleMerchant = "merchant"
	RoleAdmin    = "admin"
)

// KYC statuses: pending → submitted → verified / rejected (rejected can resubmit).
const (
	KYCPending   = "pending"
	KYCSubmitted = "submitted"
	KYCVerified  = "verified"
	KYCRejected  = "rejected"
)

// User is one account, stored in the "users" collection.
type User struct {
	ID           bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Email        string        `bson:"email" json:"email"`
	PasswordHash string        `bson:"password_hash" json:"-"` // bcrypt hash, never sent to clients
	Role         string        `bson:"role" json:"role"`
	KYC          KYC           `bson:"kyc" json:"kyc"`
	CreatedAt    time.Time     `bson:"created_at" json:"created_at"`
}

// KYC holds the identity check. The document number is sensitive,
// so it is stored encrypted; only the last 4 characters are kept readable.
type KYC struct {
	Status            string    `bson:"status" json:"status"`
	FullName          string    `bson:"full_name,omitempty" json:"full_name,omitempty"`
	DocumentType      string    `bson:"document_type,omitempty" json:"document_type,omitempty"`
	DocumentNumberEnc string    `bson:"document_number_enc,omitempty" json:"-"` // AES-GCM encrypted
	DocumentLast4     string    `bson:"document_last4,omitempty" json:"document_last4,omitempty"`
	SubmittedAt       time.Time `bson:"submitted_at,omitempty" json:"submitted_at,omitzero"`
	ReviewedAt        time.Time `bson:"reviewed_at,omitempty" json:"reviewed_at,omitzero"`
	ReviewedBy        string    `bson:"reviewed_by,omitempty" json:"-"`
	RejectReason      string    `bson:"reject_reason,omitempty" json:"reject_reason,omitempty"`
}
