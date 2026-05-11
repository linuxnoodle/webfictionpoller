package handlers

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		"ratingInit": func(f float64) string {
			if f < 0 {
				return `ratingInput($el); $el.type='text'; $el.value='U'; $el.dataset.u='1'`
			}
			s := strconv.FormatFloat(f, 'f', 1, 64)
			return fmt.Sprintf("ratingInput($el); $el.type='number'; $el.value='%s'; $el.dataset.u='0'", s)
		},
		"ratingSave": func(sidVar string) string {
			return fmt.Sprintf(`$nextTick(()=>{ var i=$el.querySelector('input'); i.dataset.sid=%s; i._ratingSave=function(r){ratingSync(i,r);htmx.ajax('POST','/api/series/'+%s+'/rating',{values:{rating:r},swap:'none'})} })`, sidVar, sidVar)
		},
		"add": func(a, b int) int {
			return a + b
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"mod": func(a, b int) int {
			return a % b
		},
		"fdiv": func(a, b int) float64 {
			return float64(a) / float64(b)
		},
		"faviconURL": func(providerName string) string {
			return models.ProviderFavicon(providerName)
		},
		"jsstr": func(s string) string {
			return strings.ReplaceAll(s, `\`, `\\`)
		},
	}).ParseFS(sub, "*.html"))
	return nil
}

func RenderLoginPage(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func RenderSetupPage(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "setup.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name+".html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
