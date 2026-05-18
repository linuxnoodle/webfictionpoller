package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/justinas/nosurf"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

var tmpl *template.Template

func InitTemplates() error {
	sub, err := fs.Sub(TemplateFS, "templates")
	if err != nil {
		return err
	}
	tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"f64f": func(f float64) string {
			if f < 0 {
				return "-1"
			}
			return strconv.FormatFloat(f, 'f', 1, 64)
		},
		"ratingStyle": func(f float64) template.HTMLAttr {
			if f < 0 {
				return ""
			}
			h := (f / 10.0) * 120.0
			return template.HTMLAttr(fmt.Sprintf(`style="color:hsl(%.0f,65%%,50%%);border-color:hsl(%.0f,65%%,50%%);background-color:hsl(%.0f,65%%,12%%)"`, h, h, h))
		},
		"ratingText": func(f float64) string {
			if f < 0 {
				return "U"
			}
			return strconv.FormatFloat(f, 'f', 1, 64)
		},
		"ratingSave": func(sidVar string) string {
			return fmt.Sprintf(`$nextTick(()=>{ var i=$el.querySelector('input'); i.dataset.sid=%s; i._ratingSave=function(r){ratingSync(i,r);htmx.ajax('POST','/api/series/'+%s+'/rating',{values:{rating:r},swap:'none'})} })`, sidVar, sidVar)
		},
		"ratingHue": func(i int) float64 {
			return float64(i) * 1.2
		},
		"add": func(a, b int) int {
			return a + b
		},
		"mod": func(a, b int) int {
			return a % b
		},
		"faviconURL": func(providerName string) string {
			return models.ProviderFavicon(providerName)
		},
		"jsstr": func(s string) string {
			b, _ := json.Marshal(s)
			return string(b)
		},
	}).ParseFS(sub, "*.html"))
	return nil
}

func RenderLoginPage(w http.ResponseWriter, r *http.Request, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := withCSRF(r, data)
	if err := tmpl.ExecuteTemplate(w, "login.html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func RenderSetupPage(w http.ResponseWriter, r *http.Request, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := withCSRF(r, data)
	if err := tmpl.ExecuteTemplate(w, "setup.html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func renderTemplate(w http.ResponseWriter, r *http.Request, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := withCSRF(r, data)
	if err := tmpl.ExecuteTemplate(w, name+".html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func withCSRF(r *http.Request, data interface{}) map[string]interface{} {
	token := nosurf.Token(r)
	if m, ok := data.(map[string]interface{}); ok {
		m["CSRFToken"] = token
		return m
	}
	return map[string]interface{}{
		"CSRFToken": token,
		"Data":      data,
	}
}

func groupByDay(chapters []models.ChapterWithSeries, sortBy string) []models.DayGroup {
	var groups []models.DayGroup
	var prevKey string

	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)

	for _, ch := range chapters {
		t := ch.PublishedAt
		if sortBy == "received" {
			t = ch.CreatedAt
		}
		key := t.Format("2006-01-02")

		if key != prevKey {
			label := t.Format("January 02, 2006")
			if key == now.Format("2006-01-02") {
				label = "Today"
			} else if key == yesterday.Format("2006-01-02") {
				label = "Yesterday"
			}
			groups = append(groups, models.DayGroup{Date: label, Chapters: []models.ChapterWithSeries{}})
			prevKey = key
		}
		groups[len(groups)-1].Chapters = append(groups[len(groups)-1].Chapters, ch)
	}
	return groups
}
