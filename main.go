package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker"
	"golang.org/x/net/html"
	"golang.org/x/time/rate"
)

// JobSearchParams represents the parameters for a job search
type JobSearchParams struct {
	Query          string                 `json:"query"`
	Locations      []string               `json:"locations"`
	Limit          int                    `json:"limit"`
	Options        map[string]interface{} `json:"options"`
	ExistingJobIds []string               `json:"existingJobIds"`
	MaxAgeDays     *int                   `json:"max_age_days,omitempty"`
}

// JobInfo represents the information about a job
type JobInfo struct {
	JobID            string   `json:"jobId"`
	Title            string   `json:"title"`
	Company          string   `json:"company"`
	CompanyLink      string   `json:"companyLink,omitempty"`
	CompanyImgLink   string   `json:"companyImgLink,omitempty"`
	Place            string   `json:"place"`
	Date             string   `json:"date,omitempty"`
	Link             string   `json:"link"`
	SeniorityLevel   string   `json:"seniorityLevel,omitempty"`
	JobFunction      string   `json:"jobFunction,omitempty"`
	EmploymentType   string   `json:"employmentType,omitempty"`
	Description      string   `json:"description"`
	DescriptionHTML  string   `json:"descriptionHTML"`
	ApplyLink        string   `json:"applyLink,omitempty"`
	CompanyApplyURL  string   `json:"companyApplyUrl,omitempty"`
	Salary           string   `json:"salary,omitempty"`
	FeaturedBenefits []string `json:"featuredBenefits,omitempty"`
}

// ScrapeTask represents a task in the queue
type ScrapeTask struct {
	Params JobSearchParams
	Result chan []JobInfo
}

// GlassdoorResponse represents the response from Glassdoor API
type GlassdoorResponse struct {
	Response struct {
		Employers []struct {
			Name            string `json:"name"`
			NumberOfRatings int    `json:"numberOfRatings"`
		} `json:"employers"`
	} `json:"response"`
}

// Adaptive Rate Limiter
type AdaptiveRateLimiter struct {
	limit     rate.Limit
	burst     int
	limiter   *rate.Limiter
	successes int
	failures  int
	mu        sync.Mutex
}

func NewAdaptiveRateLimiter(initialLimit rate.Limit, burst int) *AdaptiveRateLimiter {
	return &AdaptiveRateLimiter{
		limit:   initialLimit,
		burst:   burst,
		limiter: rate.NewLimiter(initialLimit, burst),
	}
}

func (a *AdaptiveRateLimiter) Wait(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.failures > 5 {
		a.limit /= 3
		a.failures = 0
		a.successes = 0
		a.limiter.SetLimit(a.limit)
	} else if a.successes > 50 {
		a.limit *= 1.2
		a.failures = 0
		a.successes = 0
		a.limiter.SetLimit(a.limit)
	}

	return a.limiter.Wait(ctx)
}

func (a *AdaptiveRateLimiter) Success() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.successes++
}

func (a *AdaptiveRateLimiter) Failure() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures++
}

var adaptiveLimiter = NewAdaptiveRateLimiter(rate.Every(5*time.Second), 1)

// Intelligent Retry Mechanism
func intelligentRetry(operation func() (int, error)) (int, error) {
	baseDelay := 1 * time.Second
	maxDelay := 1 * time.Hour
	maxRetries := 10

	for i := 0; i < maxRetries; i++ {
		statusCode, err := operation()
		if err == nil {
			return statusCode, nil
		}

		if i == maxRetries-1 {
			return statusCode, err
		}

		if statusCode == 429 || strings.Contains(err.Error(), "rate limit") {
			delay := time.Duration(math.Pow(2, float64(i))) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}
			jitter := time.Duration(rand.Int63n(int64(delay) / 2))
			delay += jitter

			log.Printf("Rate limited. Retrying in %v", delay)
			time.Sleep(delay)
		} else {
			return statusCode, err
		}
	}

	return 0, fmt.Errorf("max retries exceeded")
}

var (
	userAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:89.0) Gecko/20100101 Firefox/89.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.1.1 Safari/605.1.15",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36 Edg/91.0.864.59",
	}

	proxies = []string{
		"p.webshare.io:80:zxvygfrs:6z2d476mdagx",
	}

	proxyIndex = 0
	proxyMutex sync.Mutex

	taskQueue     chan ScrapeTask
	workerCount   = 20
	maxQueueSize  = 100
	workerControl = make(chan bool, workerCount)

	limiter = rate.NewLimiter(rate.Every(time.Second), 5) // 5 requests per second

	httpClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second,
	}

	cb *gobreaker.CircuitBreaker

	glassdoorCache      = make(map[string]bool)
	glassdoorCacheMutex sync.RWMutex
	glassdoorPartnerID  = "233203"
	glassdoorPartnerKey = "jrTfWk5uhyu"
	ratingThreshold     = 5
)

// Global rate limiter for job status checks
var jobStatusLimiter = rate.NewLimiter(rate.Every(5*time.Second), 1)

// Circuit breaker for job status checks
var jobStatusCB *gobreaker.CircuitBreaker

func init() {
	taskQueue = make(chan ScrapeTask, maxQueueSize)
	for i := 0; i < workerCount; i++ {
		go worker(i)
	}

	var st gobreaker.Settings
	st.Name = "LinkedIn"
	st.MaxRequests = 5
	st.Interval = 5 * time.Second
	st.Timeout = 30 * time.Second
	st.ReadyToTrip = func(counts gobreaker.Counts) bool {
		failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
		return counts.Requests >= 3 && failureRatio >= 0.6
	}
	cb = gobreaker.NewCircuitBreaker(st)
}

func worker(id int) {
	for task := range taskQueue {
		log.Printf("Worker %d processing task", id)
		jobs, err := scrapeJobs(task.Params)
		if err != nil {
			log.Printf("Worker %d encountered error: %v", id, err)
			task.Result <- nil
		} else {
			task.Result <- jobs
		}
		<-workerControl
	}
}

func getNextProxy() string {
	proxyMutex.Lock()
	defer proxyMutex.Unlock()
	proxy := proxies[proxyIndex]
	proxyIndex = (proxyIndex + 1) % len(proxies)
	return proxy
}

func buildLinkedinURL(params JobSearchParams) string {
	baseURL := "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?"
	queryParams := url.Values{}
	queryParams.Set("keywords", params.Query)
	if len(params.Locations) > 0 {
		queryParams.Set("location", strings.Join(params.Locations, ","))
	}
	queryParams.Set("sortBy", "DD")
	queryParams.Set("start", "0")
	// Add nil check for Options
	if params.Options != nil {
		// Add type assertion check for filters
		if filtersInterface, exists := params.Options["filters"]; exists && filtersInterface != nil {
			filters, ok := filtersInterface.(map[string]interface{})
			if ok {
				// Handle job types
				if typesInterface, exists := filters["type"]; exists && typesInterface != nil {
					if types, ok := typesInterface.([]interface{}); ok && len(types) > 0 {
						jobTypes := make([]string, 0)
						for _, t := range types {
							if typeStr, ok := t.(string); ok {
								jobTypes = append(jobTypes, typeStr)
							}
						}
						if len(jobTypes) > 0 {
							queryParams.Set("f_JT", strings.Join(jobTypes, ","))
						}
					}
				}
				// Handle experience levels
				if expInterface, exists := filters["experience"]; exists && expInterface != nil {
					if experience, ok := expInterface.([]interface{}); ok && len(experience) > 0 {
						expLevels := make([]string, 0)
						for _, e := range experience {
							if expStr, ok := e.(string); ok {
								expLevels = append(expLevels, expStr)
							}
						}
						if len(expLevels) > 0 {
							queryParams.Set("f_E", strings.Join(expLevels, ","))
						}
					}
				}
				// Handle remote/onsite preferences
				if remoteInterface, exists := filters["onSiteOrRemote"]; exists && remoteInterface != nil {
					if onSiteOrRemote, ok := remoteInterface.([]interface{}); ok && len(onSiteOrRemote) > 0 {
						remoteTypes := make([]string, 0)
						for _, r := range onSiteOrRemote {
							if remoteStr, ok := r.(string); ok {
								switch remoteStr {
								case "ON_SITE":
									remoteTypes = append(remoteTypes, "1")
								case "REMOTE":
									remoteTypes = append(remoteTypes, "2")
								case "HYBRID":
									remoteTypes = append(remoteTypes, "3")
								}
							}
						}
						if len(remoteTypes) > 0 {
							queryParams.Set("f_WRA", strings.Join(remoteTypes, ","))
						}
					}
				}
			}
		}
	}
	return baseURL + queryParams.Encode()
}

func fetchPage(url string, retries int, minDelay, maxDelay, postLoadDelay time.Duration) (string, error) {
	var lastErr error
	backoff := minDelay

	for attempt := 0; attempt < retries; attempt++ {
		proxyURL := getNextProxy()
		client := createClientWithProxy(proxyURL)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}

		// Enhanced headers to look more like a real browser
		userAgent := userAgents[rand.Intn(len(userAgents))]
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.5")
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "none")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Cache-Control", "max-age=0")

		log.Printf("Attempt %d: Fetching URL: %s with User-Agent: %s", attempt+1, url, userAgent)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Request error on attempt %d: %v", attempt+1, err)
			lastErr = err
			time.Sleep(backoff)
			backoff = calculateBackoff(backoff, maxDelay)
			continue
		}

		log.Printf("Response status code: %d", resp.StatusCode)

		defer resp.Body.Close()

		// Read response body
		var bodyBytes []byte
		if resp.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(resp.Body)
			if err != nil {
				log.Printf("Error creating gzip reader: %v", err)
				continue
			}
			defer reader.Close()
			bodyBytes, err = ioutil.ReadAll(reader)
		} else {
			bodyBytes, err = ioutil.ReadAll(resp.Body)
		}

		if err != nil {
			log.Printf("Error reading response body: %v", err)
			continue
		}

		body := string(bodyBytes)

		if resp.StatusCode == 429 {
			adaptiveLimiter.Failure()
			retryAfter := resp.Header.Get("Retry-After")
			sleepDuration := parseRetryAfter(retryAfter)
			if sleepDuration == 0 {
				sleepDuration = backoff
			}
			log.Printf("Rate limited (429). Sleeping for %v", sleepDuration)
			time.Sleep(sleepDuration)
			backoff = calculateBackoff(backoff, maxDelay)
			continue
		}

		if resp.StatusCode != 200 {
			log.Printf("Unexpected status code %d", resp.StatusCode)
			continue
		}

		// Check if the response contains a CAPTCHA or block page
		if strings.Contains(body, "captcha") || strings.Contains(body, "blocked") || strings.Contains(body, "denied") {
			log.Printf("Detected CAPTCHA/blocking page")
			adaptiveLimiter.Failure()
			time.Sleep(backoff)
			backoff = calculateBackoff(backoff, maxDelay)
			continue
		}

		adaptiveLimiter.Success()
		return body, nil
	}

	return "", fmt.Errorf("max retries exceeded: %v", lastErr)
}

func createClientWithProxy(proxyStr string) *http.Client {
	parts := strings.Split(proxyStr, ":")
	proxyURL := fmt.Sprintf("http://%s:%s@%s:%s", parts[2], parts[3], parts[0], parts[1])

	log.Printf("Setting up proxy with URL: %s", strings.Replace(proxyURL, parts[3], "****", 1))

	proxy, err := url.Parse(proxyURL)
	if err != nil {
		log.Printf("Error parsing proxy URL: %v", err)
		return httpClient
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxy),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	// Test the proxy connection
	testResp, err := client.Get("https://ipv4.webshare.io/")
	if err != nil {
		log.Printf("Proxy test failed: %v", err)
		return httpClient
	}
	defer testResp.Body.Close()

	if testResp.StatusCode != 200 {
		log.Printf("Proxy test failed with status code: %d", testResp.StatusCode)
		return httpClient
	}

	log.Printf("Proxy test successful")
	return client
}

func safeFindString(doc *goquery.Document, selector string) string {
	return doc.Find(selector).First().Text()
}

func safeFindAttribute(doc *goquery.Document, selector, attr string) string {
	if s := doc.Find(selector).First(); s.Length() > 0 {
		if val, exists := s.Attr(attr); exists {
			return val
		}
	}
	return ""
}

func paginateJobSearch(baseURL string, requiredJobs, maxPages int) ([]JobInfo, error) {
	var allJobs []JobInfo
	var mu sync.Mutex
	var wg sync.WaitGroup
	errors := make(chan error, maxPages)
	semaphore := make(chan struct{}, 3) // Limit concurrent requests

	for page := 0; page < maxPages && len(allJobs) < requiredJobs; page++ {
		wg.Add(1)
		go func(pageNum int) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			url := fmt.Sprintf("%s&start=%d", baseURL, pageNum*25)
			pageContent, err := fetchPage(url, 5, 1*time.Second, 3*time.Second, 2*time.Second)
			if err != nil {
				errors <- err
				return
			}

			jobs := parseJobsFromPage(pageContent)

			mu.Lock()
			allJobs = append(allJobs, jobs...)
			mu.Unlock()
		}(page)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		log.Printf("Error during pagination: %v", err)
	}

	return allJobs, nil
}

func isWellKnownCompany(company string) bool {
	glassdoorCacheMutex.RLock()
	if isWellKnown, exists := glassdoorCache[company]; exists {
		glassdoorCacheMutex.RUnlock()
		return isWellKnown
	}
	glassdoorCacheMutex.RUnlock()

	apiURL := fmt.Sprintf("https://api.glassdoor.com/api/api.htm?v=1&format=json&t.p=%s&t.k=%s&action=employers&q=%s",
		glassdoorPartnerID, glassdoorPartnerKey, url.QueryEscape(company))

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Error fetching Glassdoor data for %s: %v", company, err)
		return false
	}
	defer resp.Body.Close()

	var glassdoorResp GlassdoorResponse
	if err := json.NewDecoder(resp.Body).Decode(&glassdoorResp); err != nil {
		log.Printf("Error decoding Glassdoor response for %s: %v", company, err)
		return false
	}

	isWellKnown := false
	if len(glassdoorResp.Response.Employers) > 0 {
		isWellKnown = glassdoorResp.Response.Employers[0].NumberOfRatings >= ratingThreshold
	}

	glassdoorCacheMutex.Lock()
	glassdoorCache[company] = isWellKnown
	glassdoorCacheMutex.Unlock()

	return isWellKnown
}

func scrapeJobs(params JobSearchParams) ([]JobInfo, error) {
	searchURL := buildLinkedinURL(params)
	jobURL := "https://www.linkedin.com/jobs-guest/jobs/api/jobPosting/%s"
	maxRetries := 4

	allJobsOnPage, err := paginateJobSearch(searchURL, params.Limit*2, 5)
	if err != nil {
		log.Printf("Error in initial job search: %v", err)
		return nil, fmt.Errorf("error in initial job search: %v", err)
	}

	if len(allJobsOnPage) < params.Limit {
		log.Println("Not enough jobs found, modifying search parameters and retrying.")
		params = modifySearchParams(params)
		searchURL = buildLinkedinURL(params)
		additionalJobs, err := paginateJobSearch(searchURL, params.Limit*2-len(allJobsOnPage), 5)
		if err != nil {
			log.Printf("Error in additional job search: %v", err)
			return nil, fmt.Errorf("error in additional job search: %v", err)
		}
		allJobsOnPage = append(allJobsOnPage, additionalJobs...)
	}

	log.Printf("Found %d job listings", len(allJobsOnPage))

	rand.Shuffle(len(allJobsOnPage), func(i, j int) {
		allJobsOnPage[i], allJobsOnPage[j] = allJobsOnPage[j], allJobsOnPage[i]
	})

	existingJobIDs := make(map[string]bool)
	for _, id := range params.ExistingJobIds {
		existingJobIDs[id] = true
	}

	jobDetailsChan := make(chan JobInfo, len(allJobsOnPage))
	errorChan := make(chan error, len(allJobsOnPage))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5) // Limit concurrent requests

	for _, job := range allJobsOnPage {
		if existingJobIDs[job.JobID] {
			log.Printf("Skipping duplicate job ID: %s", job.JobID)
			continue
		}

		wg.Add(1)
		go func(job JobInfo) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			jobDetails, err := fetchJobDetails(job, jobURL, maxRetries)
			if err != nil {
				errorChan <- fmt.Errorf("error fetching job details for job ID %s: %v", job.JobID, err)
				return
			}
			jobDetailsChan <- jobDetails
		}(job)
	}

	go func() {
		wg.Wait()
		close(jobDetailsChan)
		close(errorChan)
	}()

	var jobDetails []JobInfo
	for job := range jobDetailsChan {
		if isWellKnownCompany(job.Company) {
			jobDetails = append(jobDetails, job)
			if len(jobDetails) >= params.Limit {
				break
			}
		}
	}

	if len(jobDetails) == 0 {
		log.Println("No job details were successfully scraped")
		return nil, fmt.Errorf("no job details were successfully scraped")
	}

	var errors []string
	for err := range errorChan {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		log.Printf("Encountered %d errors while fetching job details", len(errors))
		for _, errStr := range errors {
			log.Println(errStr)
		}
	}

	log.Printf("Successfully scraped %d jobs", len(jobDetails))

	return jobDetails, nil
}

func fetchJobDetails(job JobInfo, jobURL string, maxRetries int) (JobInfo, error) {
	detailRetryCount := 0
	for detailRetryCount < maxRetries {
		jobDetailContent, err := fetchPage(fmt.Sprintf(jobURL, job.JobID), 5, 1*time.Second, 3*time.Second, 2*time.Second)
		if err != nil {
			detailRetryCount++
			log.Printf("Error fetching job details for job ID: %s. Retry %d/%d. Error: %v", job.JobID, detailRetryCount, maxRetries, err)
			time.Sleep(time.Duration(rand.Intn(5)+5) * time.Second)
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(jobDetailContent))
		if err != nil {
			detailRetryCount++
			log.Printf("Error parsing job details HTML for job ID: %s. Retry %d/%d. Error: %v", job.JobID, detailRetryCount, maxRetries, err)
			continue
		}

		descriptionElement := doc.Find("div.description__text")
		job.Description = strings.TrimSpace(descriptionElement.Text())
		job.DescriptionHTML, _ = descriptionElement.Html()

		job.CompanyLink = safeFindAttribute(doc, "a.topcard__org-name-link", "href")
		job.CompanyImgLink = safeFindAttribute(doc, "img.artdeco-entity-image", "data-delayed-url")
		job.SeniorityLevel = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Seniority level') span.description__job-criteria-text"))
		job.EmploymentType = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Employment type') span.description__job-criteria-text"))
		job.JobFunction = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Job function') span.description__job-criteria-text"))

		// Add salary information
		job.Salary = strings.TrimSpace(safeFindString(doc, "div.salary.compensation__salary"))

		// Add featured benefits
		var benefits []string
		doc.Find("li.featured-benefits__list-item .benefit__text").Each(func(i int, s *goquery.Selection) {
			benefit := strings.TrimSpace(s.Text())
			if benefit != "" {
				benefits = append(benefits, benefit)
			}
		})
		job.FeaturedBenefits = benefits

		// Fetch ApplyLink and CompanyApplyURL
		applyLink, err := fetchApplyLink(job.Link)
		if err != nil {
			log.Printf("Error fetching apply link for job %s: %v", job.JobID, err)
		} else {
			job.CompanyApplyURL = applyLink
			job.ApplyLink = applyLink
		}

		job.Title = strings.Join(strings.Fields(job.Title), " ")
		job.Company = strings.Join(strings.Fields(job.Company), " ")

		if job.Description != "" && job.DescriptionHTML != "" {
			log.Printf("Successfully fetched details for job: %s at %s", job.Title, job.Company)
			return job, nil
		} else {
			log.Printf("Skipping job ID: %s due to missing description", job.JobID)
			return JobInfo{}, fmt.Errorf("missing description for job ID: %s", job.JobID)
		}
	}

	return JobInfo{}, fmt.Errorf("failed to fetch job details for job ID: %s after %d attempts", job.JobID, maxRetries)
}

func modifySearchParams(params JobSearchParams) JobSearchParams {
	if filters, ok := params.Options["filters"].(map[string]interface{}); ok {
		if types, ok := filters["type"].([]interface{}); ok {
			found := false
			for _, t := range types {
				if t == "FULL_TIME" {
					found = true
					break
				}
			}
			if !found {
				types = append(types, "FULL_TIME")
			}
			filters["type"] = types
		}

		if experience, ok := filters["experience"].([]interface{}); !ok || len(experience) == 0 {
			filters["experience"] = []interface{}{"ENTRY_LEVEL"}
		}
	} else {
		params.Options["filters"] = map[string]interface{}{
			"type":       []interface{}{"FULL_TIME"},
			"experience": []interface{}{"ENTRY_LEVEL"},
		}
	}
	log.Println("Modified search parameters to broaden the search by adjusting filters.")
	return params
}

func fetchApplyLink(jobURL string) (string, error) {
	fullJobURL := jobURL
	if !strings.HasPrefix(jobURL, "https://www.linkedin.com/jobs/view/") {
		fullJobURL = fmt.Sprintf("https://www.linkedin.com/jobs/view/%s", strings.Split(jobURL, "/")[len(strings.Split(jobURL, "/"))-1])
	}

	retries := 3
	delay := time.Second

	for attempt := 0; attempt < retries; attempt++ {
		log.Printf("Attempt %d/%d: Fetching apply link for %s", attempt+1, retries, fullJobURL)

		content, err := fetchPage(fullJobURL, 5, 1*time.Second, 3*time.Second, 2*time.Second)
		if err != nil {
			if attempt < retries-1 {
				delay = time.Duration(math.Min(60, math.Pow(2, float64(attempt)))) * time.Second
				log.Printf("Error fetching apply link for %s. Retrying in %v seconds...", fullJobURL, delay.Seconds())
				time.Sleep(delay)
				continue
			}
			return "", fmt.Errorf("failed to fetch apply link for %s after %d attempts: %v", fullJobURL, retries, err)
		}

		re := regexp.MustCompile(`<code id="bpr-guid-\d+">(.*?)</code>`)
		match := re.FindStringSubmatch(content)
		if len(match) > 1 {
			jsonData := html.UnescapeString(match[1])
			var data map[string]interface{}
			err := json.Unmarshal([]byte(jsonData), &data)
			if err == nil {
				if applyMethod, ok := data["data"].(map[string]interface{})["applyMethod"].(map[string]interface{}); ok {
					if companyApplyURL, ok := applyMethod["companyApplyUrl"].(string); ok {
						log.Printf("Found companyApplyUrl for %s: %s", fullJobURL, companyApplyURL)
						return extractExternalURL(companyApplyURL), nil
					}
				}
			}
		}

		applyURLRe := regexp.MustCompile(`<code id="applyUrl" style="display: none"><!--"(.*?)"--></code>`)
		applyURLMatch := applyURLRe.FindStringSubmatch(content)
		if len(applyURLMatch) > 1 {
			applyURL := html.UnescapeString(applyURLMatch[1])
			log.Printf("Found apply URL from code for %s: %s", fullJobURL, applyURL)
			return extractExternalURL(applyURL), nil
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
		if err == nil {
			applyButton := doc.Find("a[data-tracking-control-name='public_jobs_apply-link-offsite']")
			if applyButton.Length() > 0 {
				applyURL, exists := applyButton.Attr("href")
				if exists {
					log.Printf("Found offsite apply button for %s: %s", fullJobURL, applyURL)
					return extractExternalURL(applyURL), nil
				}
			}

			alternativeApplyButton := doc.Find("a[data-tracking-control-name='public_jobs_apply-link']")
			if alternativeApplyButton.Length() > 0 {
				applyURL, exists := alternativeApplyButton.Attr("href")
				if exists {
					log.Printf("Found alternative apply button for %s: %s", fullJobURL, applyURL)
					return extractExternalURL(applyURL), nil
				}
			}
		}

		log.Printf("No apply link found for %s", fullJobURL)
		return "", nil
	}

	return "", fmt.Errorf("failed to fetch apply link for %s after %d attempts", fullJobURL, retries)
}

func extractExternalURL(urlStr string) string {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		log.Printf("Error parsing URL: %v", err)
		return urlStr
	}

	if strings.Contains(parsedURL.Host, "linkedin.com") && strings.Contains(parsedURL.Path, "/jobs/view/externalApply/") {
		queryParams, err := url.ParseQuery(parsedURL.RawQuery)
		if err != nil {
			log.Printf("Error parsing query parameters: %v", err)
			return urlStr
		}

		externalURL := queryParams.Get("url")
		if externalURL != "" {
			decodedURL, err := url.QueryUnescape(externalURL)
			if err != nil {
				log.Printf("Error decoding external URL: %v", err)
				return externalURL
			}
			return decodedURL
		}
	}

	return urlStr
}

func cleanField(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`^(Seniority level|Job function|Employment type):\s*`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return s
}

func sanitizeString(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)

	s = strings.Replace(s, "\\", "\\\\", -1)
	s = strings.Replace(s, "\"", "\\\"", -1)
	s = strings.Replace(s, "\n", "\\n", -1)
	s = strings.Replace(s, "\r", "\\r", -1)
	s = strings.Replace(s, "\t", "\\t", -1)
	s = strings.Replace(s, "\f", "\\f", -1)
	s = strings.Replace(s, "\b", "\\b", -1)

	return cleanField(s)
}

func scrapeLinkedinJobs(c *gin.Context) {
	var searchParams JobSearchParams
	if err := c.ShouldBindJSON(&searchParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		task := ScrapeTask{
			Params: searchParams,
			Result: make(chan []JobInfo, 1),
		}

		select {
		case taskQueue <- task:
			workerControl <- true // Acquire worker control
		default:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Server is busy, please try again later"})
			return
		}

		select {
		case result := <-task.Result:
			if result == nil {
				log.Printf("Attempt %d: Failed to scrape jobs", attempt+1)
				if attempt == maxRetries-1 {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scrape jobs after multiple attempts"})
					return
				}
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			if len(result) == 0 {
				log.Println("No jobs found matching the criteria")
				c.JSON(http.StatusNotFound, gin.H{"error": "No jobs found matching the criteria"})
				return
			}
			if len(result) > searchParams.Limit {
				result = result[:searchParams.Limit]
			}
			log.Printf("Returning %d jobs to client", len(result))

			for i := range result {
				result[i].JobID = sanitizeString(result[i].JobID)
				result[i].Title = sanitizeString(result[i].Title)
				result[i].Company = sanitizeString(result[i].Company)
				result[i].CompanyLink = sanitizeString(result[i].CompanyLink)
				result[i].CompanyImgLink = sanitizeString(result[i].CompanyImgLink)
				result[i].Place = sanitizeString(result[i].Place)
				result[i].Date = sanitizeString(result[i].Date)
				result[i].Link = sanitizeString(result[i].Link)
				result[i].SeniorityLevel = sanitizeString(result[i].SeniorityLevel)
				result[i].JobFunction = sanitizeString(result[i].JobFunction)
				result[i].EmploymentType = sanitizeString(result[i].EmploymentType)
				result[i].Description = sanitizeString(result[i].Description)
				result[i].DescriptionHTML = sanitizeString(result[i].DescriptionHTML)
				result[i].ApplyLink = sanitizeString(result[i].ApplyLink)
				result[i].CompanyApplyURL = sanitizeString(result[i].CompanyApplyURL)
			}

			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			encoder.SetIndent("", "  ")

			response := map[string]interface{}{
				"message": "Jobs scraped successfully",
				"jobs":    result,
			}
			if err := encoder.Encode(response); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode response"})
				return
			}

			c.Header("Content-Type", "application/json")
			c.String(http.StatusOK, buf.String())
			return

		case <-time.After(600 * time.Second):
			log.Printf("Attempt %d: Request timed out", attempt+1)
			if attempt == maxRetries-1 {
				c.JSON(http.StatusRequestTimeout, gin.H{"error": "Request timed out after multiple attempts"})
				return
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
}

// Add this new struct
type JobStatusRequest struct {
	JobID string `json:"jobId" binding:"required"`
}

// Add this new function
func checkJobStatus(c *gin.Context) {
	var request JobStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	jobURL := fmt.Sprintf("https://www.linkedin.com/jobs/view/%s", request.JobID)

	statusCode, err := intelligentRetry(func() (int, error) {
		if err := adaptiveLimiter.Wait(context.Background()); err != nil {
			return 0, err
		}

		result, err := cb.Execute(func() (interface{}, error) {
			return fetchJobStatusWithStatusCode(jobURL)
		})

		if err != nil {
			adaptiveLimiter.Failure()
			if err == gobreaker.ErrOpenState {
				return 0, fmt.Errorf("circuit breaker is open")
			}
			return 0, err
		}

		statusCode := result.(int)
		adaptiveLimiter.Success()
		return statusCode, nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	switch statusCode {
	case 200:
		c.JSON(http.StatusOK, gin.H{"active": true})
	case 404:
		c.JSON(http.StatusOK, gin.H{"active": false})
	case 999:
		c.JSON(http.StatusOK, gin.H{"active": false})
	default:
		c.JSON(http.StatusOK, gin.H{"active": true, "note": fmt.Sprintf("Unexpected status code: %d", statusCode)})
	}
}

// Modified fetchJobStatusWithStatusCode function
func fetchJobStatusWithStatusCode(url string) (int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("User-Agent", userAgents[rand.Intn(len(userAgents))])

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	log.Printf("HTTP request to %s returned status code %d", url, resp.StatusCode)

	if resp.StatusCode == 429 {
		return 429, fmt.Errorf("rate limited")
	}

	return resp.StatusCode, nil
}

// New function to fetch page and return status code
func fetchPageWithStatusCode(url string, retries int, minDelay, maxDelay, postLoadDelay time.Duration) (string, int, error) {
	backoff := minDelay

	for attempt := 0; attempt < retries; attempt++ {
		// Wait for rate limiter
		if err := limiter.Wait(context.Background()); err != nil {
			return "", 0, fmt.Errorf("rate limiter error: %v", err)
		}

		// Use the circuit breaker
		result, err := cb.Execute(func() (interface{}, error) {
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return nil, err
			}

			req.Header.Set("User-Agent", userAgents[rand.Intn(len(userAgents))])

			log.Printf("Attempt %d/%d: Fetching URL %s", attempt+1, retries, url)

			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			log.Printf("HTTP request to %s returned status code %d", url, resp.StatusCode)

			if resp.StatusCode == 429 {
				retryAfter := resp.Header.Get("Retry-After")
				sleepDuration := parseRetryAfter(retryAfter)
				if sleepDuration == 0 {
					sleepDuration = calculateBackoff(backoff, maxDelay)
				}
				log.Printf("Rate limited. Sleeping for %v before retrying...", sleepDuration)
				time.Sleep(sleepDuration)
				backoff = sleepDuration
				return nil, fmt.Errorf("rate limited")
			}

			// Wait for the page to "load"
			log.Printf("Waiting %v for page to load...", postLoadDelay)
			time.Sleep(postLoadDelay)

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}

			return map[string]interface{}{
				"body":       string(body),
				"statusCode": resp.StatusCode,
			}, nil
		})

		if err != nil {
			if attempt < retries-1 {
				backoff = calculateBackoff(backoff, maxDelay)
				log.Printf("Request failed. Retrying in %v", backoff)
				time.Sleep(backoff)
				continue
			}
			return "", 0, err
		}

		// Type assert the result
		resultMap, ok := result.(map[string]interface{})
		if !ok {
			return "", 0, fmt.Errorf("unexpected result type from circuit breaker")
		}

		body, ok := resultMap["body"].(string)
		if !ok {
			return "", 0, fmt.Errorf("body is not a string")
		}

		statusCode, ok := resultMap["statusCode"].(int)
		if !ok {
			return "", 0, fmt.Errorf("statusCode is not an int")
		}

		return body, statusCode, nil
	}

	return "", 429, fmt.Errorf("failed to fetch page after %d attempts", retries)
}

func calculateBackoff(current, max time.Duration) time.Duration {
	backoff := current * 2
	if backoff > max {
		backoff = max
	}
	// Add more jitter
	jitter := time.Duration(rand.Int63n(int64(backoff)))
	return backoff + jitter
}

func parseRetryAfter(retryAfter string) time.Duration {
	if retryAfter == "" {
		return 0
	}
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		log.Printf("Failed to parse Retry-After header: %v", err)
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func parseJobsFromPage(pageContent string) []JobInfo {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageContent))
	if err != nil {
		log.Printf("Error parsing HTML: %v", err)
		return nil
	}

	var jobs []JobInfo
	doc.Find("li .job-search-card").Each(func(i int, s *goquery.Selection) {
		job := JobInfo{}

		// Extract job ID from the data-entity-urn attribute
		if entityUrn, exists := s.Attr("data-entity-urn"); exists {
			parts := strings.Split(entityUrn, ":")
			if len(parts) > 0 {
				job.JobID = parts[len(parts)-1]
			}
		}

		// Extract job title
		job.Title = strings.TrimSpace(s.Find(".base-search-card__title").Text())

		// Extract company name and link
		companyElem := s.Find(".base-search-card__subtitle a")
		job.Company = strings.TrimSpace(companyElem.Text())
		if companyLink, exists := companyElem.Attr("href"); exists {
			job.CompanyLink = companyLink
		}

		// Extract company image
		if imgSrc, exists := s.Find(".search-entity-media img").Attr("data-delayed-url"); exists {
			job.CompanyImgLink = imgSrc
		}

		// Extract location
		job.Place = strings.TrimSpace(s.Find(".job-search-card__location").Text())

		// Extract posting date
		dateElem := s.Find(".job-search-card__listdate")
		if dateStr, exists := dateElem.Attr("datetime"); exists {
			job.Date = dateStr
		}

		// Extract job link
		if link, exists := s.Find("a.base-card__full-link").Attr("href"); exists {
			job.Link = strings.Split(link, "?")[0] // Remove query parameters
		}

		// Only append jobs that have at least an ID and title
		if job.JobID != "" && job.Title != "" {
			log.Printf("Found job: %s at %s", job.Title, job.Company)
			jobs = append(jobs, job)
		}
	})

	log.Printf("Successfully parsed %d jobs from page", len(jobs))
	return jobs
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}
	r.Use(cors.New(config))

	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	r.POST("/scrape", scrapeLinkedinJobs)

	// Add this new endpoint
	r.POST("/check-job-status", checkJobStatus)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exiting")
}
