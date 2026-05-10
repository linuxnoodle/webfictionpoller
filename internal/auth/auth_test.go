package auth

import (
	"database/sql"
	"os"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	db, err := database.InitDB(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateUser(t *testing.T) {
	db := setupTestDB(t)

	err := CreateUser(db, "testuser", "password123")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	err = CreateUser(db, "testuser", "password123")
	if err == nil {
		t.Fatal("expected error for duplicate username")
	}
}

func TestAuthenticate(t *testing.T) {
	db := setupTestDB(t)

	err := CreateUser(db, "testuser", "password123")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	tests := []struct {
		name     string
		username string
		password string
		wantErr  bool
	}{
		{"correct credentials", "testuser", "password123", false},
		{"wrong password", "testuser", "wrong", true},
		{"unknown user", "nobody", "password123", true},
		{"empty password", "testuser", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Authenticate(db, tt.username, tt.password)
			if (err != nil) != tt.wantErr {
				t.Errorf("Authenticate(%q, %q) error = %v, wantErr %v", tt.username, tt.password, err, tt.wantErr)
			}
		})
	}
}

func TestEnsureDefaultUser(t *testing.T) {
	db := setupTestDB(t)

	err := EnsureDefaultUser(db, "admin", "admin123")
	if err != nil {
		t.Fatalf("first EnsureDefaultUser failed: %v", err)
	}

	_, err = Authenticate(db, "admin", "admin123")
	if err != nil {
		t.Fatalf("authenticate after ensure failed: %v", err)
	}

	err = EnsureDefaultUser(db, "admin", "newpassword")
	if err != nil {
		t.Fatalf("second EnsureDefaultUser failed: %v", err)
	}

	_, err = Authenticate(db, "admin", "admin123")
	if err != nil {
		t.Fatalf("password should not change on second ensure: %v", err)
	}
}
