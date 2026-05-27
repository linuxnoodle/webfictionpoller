package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func main() {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req, _ := http.NewRequest("GET", "https://forum.questionablequesting.com/threads/weaver-option-warhammer-40k-worm.16436/threadmarks", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer resp.Body.Close()
	
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	
	fmt.Println("Title:", doc.Find("title").Text())
	
	count := 0
	doc.Find(".structItem--threadmark, .threadmarkItem").Each(func(i int, s *goquery.Selection) {
		title := strings.TrimSpace(s.Find(".structItem-title a, .threadmarkTitle").Text())
		link, _ := s.Find(".structItem-title a, a.threadmarkTitle").Attr("href")
		fmt.Printf("Ch: %s (%s)\n", title, link)
		count++
	})
	fmt.Println("Count:", count)
}
