package auth

import (
	"fmt"

	"github.com/linuxnoodle/webfictionpoller/internal/db"
	"golang.org/x/crypto/bcrypt"
)

func CreateUser(database *db.DB, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	_, err = database.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hash))
	return err
}

func Authenticate(database *db.DB, username, password string) (int64, error) {
	var id int64
	var hash string
	err := database.QueryRow("SELECT id, password_hash FROM users WHERE username = ?", username).Scan(&id, &hash)
	if err != nil {
		return 0, fmt.Errorf("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return 0, fmt.Errorf("invalid credentials")
	}
	return id, nil
}

func HasUsers(database *db.DB) (bool, error) {
	var count int
	err := database.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count > 0, err
}
