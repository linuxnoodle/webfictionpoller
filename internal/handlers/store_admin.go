package handlers

import (
	"database/sql"
	"strings"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func (s *Store) GetProviderConfig(name string) (*models.ProviderConfig, error) {
	var pc models.ProviderConfig
	err := s.db.QueryRow(`
		SELECT id, provider_name, cookie_data, last_polled, username, encrypted_password, login_tested
		FROM provider_configs WHERE provider_name = ?
	`, name).Scan(&pc.ID, &pc.ProviderName, &pc.CookieData, &pc.LastPolled, &pc.Username, &pc.EncryptedPassword, &pc.LoginTested)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pc, nil
}

func (s *Store) UpsertProviderConfig(name, cookieData, username, encryptedPassword string) error {
	_, err := s.db.Exec(`
		INSERT INTO provider_configs (provider_name, cookie_data, last_polled, username, encrypted_password, login_tested)
		VALUES (?, ?, ?, ?, ?, 0)
		ON CONFLICT(provider_name) DO UPDATE SET
			cookie_data = excluded.cookie_data,
			last_polled = excluded.last_polled,
			username = excluded.username,
			encrypted_password = excluded.encrypted_password,
			login_tested = CASE WHEN provider_configs.encrypted_password IS NOT excluded.encrypted_password THEN 0 ELSE provider_configs.login_tested END
	`, name, cookieData, time.Now(), username, encryptedPassword)
	return err
}

func (s *Store) SetLoginTested(name string, tested bool) error {
	_, err := s.db.Exec("UPDATE provider_configs SET login_tested = ? WHERE provider_name = ?", tested, name)
	return err
}

func (s *Store) ExportBackup() (*models.Backup, error) {
	seriesList, err := s.ListSeries()
	if err != nil {
		return nil, err
	}

	backup := &models.Backup{
		Version:    1,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Series:     make([]models.SeriesBackup, 0, len(seriesList)),
	}

	for _, ser := range seriesList {
		rows, err := s.db.Query(`
			SELECT title, url, published_at, is_read
			FROM chapters WHERE series_id = ?
			ORDER BY published_at ASC
		`, ser.ID)
		if err != nil {
			return nil, err
		}

		var chapters []models.ChapterBackup
		for rows.Next() {
			var ch models.ChapterBackup
			var publishedAt sql.NullString
			var isRead bool
			if err := rows.Scan(&ch.Title, &ch.URL, &publishedAt, &isRead); err != nil {
				rows.Close()
				return nil, err
			}
			if publishedAt.Valid {
				ch.PublishedAt = publishedAt.String
			}
			ch.IsRead = isRead
			chapters = append(chapters, ch)
		}
		rows.Close()

		backup.Series = append(backup.Series, models.SeriesBackup{
			Title:        ser.Title,
			Author:       ser.Author,
			SourceURL:    ser.SourceURL,
			ProviderName: ser.ProviderName,
			Rating:       ser.Rating,
			Status:       ser.Status,
			Summary:      ser.Summary,
			ImageURL:     ser.ImageURL,
			Archive:      ser.Archive,
			Chapters:     chapters,
		})
	}

	prows, err := s.db.Query(`SELECT provider_name, cookie_data FROM provider_configs`)
	if err == nil {
		backup.Providers = make(map[string]string)
		for prows.Next() {
			var name, data string
			if prows.Scan(&name, &data) == nil && data != "" {
				backup.Providers[name] = data
			}
		}
		prows.Close()
	}

	return backup, nil
}

func (s *Store) ImportBackup(backup *models.Backup) (imported, skipped int, err error) {
	for _, sb := range backup.Series {
		existing, qerr := s.GetSeriesBySourceURL(sb.SourceURL)
		if qerr != nil {
			return imported, skipped, qerr
		}
		if existing != nil {
			if err := s.UpdateSeriesRating(existing.ID, sb.Rating); err != nil {
				return imported, skipped, err
			}
			if err := s.UpdateSeriesStatus(existing.ID, sb.Status); err != nil {
				return imported, skipped, err
			}
			skipped++
			continue
		}

		ser := models.Series{
			Title:        sb.Title,
			Author:       sb.Author,
			SourceURL:    sb.SourceURL,
			ProviderName: sb.ProviderName,
			Rating:       sb.Rating,
			Status:       sb.Status,
			Summary:      sb.Summary,
			ImageURL:     sb.ImageURL,
			Archive:      sb.Archive,
		}
		id, aerr := s.AddSeries(ser)
		if aerr != nil {
			if strings.Contains(aerr.Error(), "UNIQUE constraint") {
				skipped++
				continue
			}
			return imported, skipped, aerr
		}

		for _, cb := range sb.Chapters {
			var publishedAt interface{}
			if cb.PublishedAt != "" {
				if t, parseErr := time.Parse(time.RFC3339, cb.PublishedAt); parseErr == nil {
					publishedAt = t
				} else {
					publishedAt = cb.PublishedAt
				}
			}
			if publishedAt == nil {
				publishedAt = time.Now()
			}
			_, cerr := s.db.Exec(`
				INSERT INTO chapters (series_id, title, url, published_at, is_read)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT DO NOTHING
			`, id, cb.Title, cb.URL, publishedAt, cb.IsRead)
			if cerr != nil {
				return imported, skipped, cerr
			}
		}
		imported++
	}

	for name, data := range backup.Providers {
		s.UpsertProviderConfig(name, data, "", "")
	}

	return imported, skipped, nil
}

func (s *Store) GetSetting(key string) string {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", key, value)
	return err
}
