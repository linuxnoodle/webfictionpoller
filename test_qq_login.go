package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"io"
	"github.com/PuerkitoBio/goquery"
)

func main() {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req1, _ := http.NewRequest("GET", "https://forum.questionablequesting.com/login/", nil)
	req1.Header.Set("User-Agent", "Mozilla/5.0")
	resp1, _ := client.Do(req1)
	doc, _ := goquery.NewDocumentFromReader(resp1.Body)
	resp1.Body.Close()
	xfToken, _ := doc.Find("input[name='_xfToken']").Attr("value")

	form := url.Values{}
	form.Set("login", "invalid_user_123")
	form.Set("password", "invalid_password_456")
	form.Set("_xfToken", xfToken)
	form.Set("remember", "1")

	req, _ := http.NewRequest("POST", "https://forum.questionablequesting.com/login/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	
	resp, _ := client.Do(req)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	doc2, _ := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	
	fmt.Println("blockMessage--error count:", doc2.Find(".blockMessage--error").Length())
	fmt.Println("errorPanel count:", doc2.Find(".errorPanel").Length())
}
