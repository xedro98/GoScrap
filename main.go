package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort" // Add this line
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
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
	JobID           string `json:"jobId"`
	Title           string `json:"title"`
	Company         string `json:"company"`
	CompanyLink     string `json:"companyLink,omitempty"`
	CompanyImgLink  string `json:"companyImgLink,omitempty"`
	Place           string `json:"place"`
	Date            string `json:"date,omitempty"`
	Link            string `json:"link"`
	SeniorityLevel  string `json:"seniorityLevel,omitempty"`
	JobFunction     string `json:"jobFunction,omitempty"`
	EmploymentType  string `json:"employmentType,omitempty"`
	Description     string `json:"description"`
	DescriptionHTML string `json:"descriptionHTML"`
	ApplyLink       string `json:"applyLink,omitempty"`
	CompanyApplyURL string `json:"companyApplyUrl,omitempty"`
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
		"45.127.248.127:5128:jvnarlhe:goxv0xi2iwdo",
		"207.244.217.165:6712:jvnarlhe:goxv0xi2iwdo",
		"134.73.69.7:5997:jvnarlhe:goxv0xi2iwdo",
		"64.64.118.149:6732:jvnarlhe:goxv0xi2iwdo",
		"157.52.253.244:6204:jvnarlhe:goxv0xi2iwdo",
		"167.160.180.203:6754:jvnarlhe:goxv0xi2iwdo",
		"166.88.58.10:5735:jvnarlhe:goxv0xi2iwdo",
		"173.0.9.70:5653:jvnarlhe:goxv0xi2iwdo",
		"204.44.69.89:6342:jvnarlhe:goxv0xi2iwdo",
		"173.0.9.209:5792:jvnarlhe:goxv0xi2iwdo",
	}

	proxyIndex = 0
	proxyMutex sync.Mutex
)

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
	queryParams.Add("keywords", params.Query)
	queryParams.Add("location", strings.Join(params.Locations, ","))
	queryParams.Add("f_TP", "")
	queryParams.Add("sortBy", "DD")
	queryParams.Add("start", "0")

	if filters, ok := params.Options["filters"].(map[string]interface{}); ok {
		if types, ok := filters["type"].([]interface{}); ok {
			jobTypes := make([]string, 0)
			for _, t := range types {
				switch t {
				case "FULL_TIME":
					jobTypes = append(jobTypes, "F")
				case "PART_TIME":
					jobTypes = append(jobTypes, "P")
				case "CONTRACT":
					jobTypes = append(jobTypes, "C")
				case "TEMPORARY":
					jobTypes = append(jobTypes, "T")
				case "VOLUNTEER":
					jobTypes = append(jobTypes, "V")
				case "INTERNSHIP":
					jobTypes = append(jobTypes, "I")
				}
			}
			queryParams.Add("f_JT", strings.Join(jobTypes, ","))
		}

		if experience, ok := filters["experience"].([]interface{}); ok {
			expLevels := make([]string, 0)
			for _, e := range experience {
				switch e {
				case "INTERNSHIP":
					expLevels = append(expLevels, "1")
				case "ENTRY_LEVEL":
					expLevels = append(expLevels, "2")
				case "ASSOCIATE":
					expLevels = append(expLevels, "3")
				case "MID_SENIOR_LEVEL":
					expLevels = append(expLevels, "4")
				case "DIRECTOR":
					expLevels = append(expLevels, "5")
				case "EXECUTIVE":
					expLevels = append(expLevels, "6")
				}
			}
			queryParams.Add("f_E", strings.Join(expLevels, ","))
		}

		if onSiteOrRemote, ok := filters["onSiteOrRemote"].([]interface{}); ok {
			remoteTypes := make([]string, 0)
			for _, r := range onSiteOrRemote {
				switch r {
				case "ON_SITE":
					remoteTypes = append(remoteTypes, "1")
				case "REMOTE":
					remoteTypes = append(remoteTypes, "2")
				case "HYBRID":
					remoteTypes = append(remoteTypes, "3")
				}
			}
			queryParams.Add("f_WRA", strings.Join(remoteTypes, ","))
		}
	}

	return baseURL + queryParams.Encode()
}

func fetchPage(url string, retries int, minDelay, maxDelay, postLoadDelay time.Duration) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second, // Increase timeout
	}

	for attempt := 0; attempt < retries; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}

		req.Header.Set("User-Agent", userAgents[rand.Intn(len(userAgents))])

		log.Printf("Attempt %d/%d: Fetching URL %s", attempt+1, retries, url)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error fetching page: %v", err)
			time.Sleep(time.Duration(math.Min(60, math.Pow(2, float64(attempt)))) * time.Second)
			continue
		}
		defer resp.Body.Close()

		log.Printf("HTTP request to %s returned status code %d", url, resp.StatusCode)

		if resp.StatusCode == 429 {
			if attempt < retries-1 {
				delay := time.Duration(math.Min(60, math.Pow(2, float64(attempt)))) * time.Second
				log.Printf("Rate limit hit. Retrying in %v seconds...", delay.Seconds())
				time.Sleep(delay)
				continue
			} else {
				return "", fmt.Errorf("rate limit hit after multiple retries")
			}
		}

		time.Sleep(postLoadDelay)

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		return string(body), nil
	}

	return "", fmt.Errorf("failed to fetch page after %d attempts", retries)
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
	page := 0

	for len(allJobs) < requiredJobs && page < maxPages {
		url := fmt.Sprintf("%s&start=%d", baseURL, page*25)
		pageContent, err := fetchPage(url, 5, 1*time.Second, 3*time.Second, 2*time.Second)
		if err != nil {
			return nil, err
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageContent))
		if err != nil {
			return nil, err
		}

		jobsOnPage := doc.Find("li")
		if jobsOnPage.Length() == 0 {
			break
		}

		jobsOnPage.Each(func(i int, s *goquery.Selection) {
			job := JobInfo{}
			baseCard := s.Find("div.base-card")
			if baseCard.Length() > 0 {
				job.JobID = baseCard.AttrOr("data-entity-urn", "")
				job.JobID = strings.TrimPrefix(job.JobID, "urn:li:jobPosting:")
				job.Link = baseCard.Find("a.base-card__full-link").AttrOr("href", "")
				job.Title = baseCard.Find("h3.base-search-card__title").Text()
				job.Company = baseCard.Find("h4.base-search-card__subtitle").Text()
				job.Place = baseCard.Find("span.job-search-card__location").Text()
				job.Date = baseCard.Find("time.job-search-card__listdate").AttrOr("datetime", "")
			}
			allJobs = append(allJobs, job)
		})

		page++
		time.Sleep(time.Duration(rand.Intn(3)+2) * time.Second)
	}

	if len(allJobs) < requiredJobs {
		return allJobs, nil
	}
	return allJobs[:requiredJobs], nil
}

func scrapeJobs(params JobSearchParams) ([]JobInfo, error) {
	searchURL := buildLinkedinURL(params)
	jobURL := "https://www.linkedin.com/jobs-guest/jobs/api/jobPosting/%s"
	maxRetries := 4

	allJobsOnPage, err := paginateJobSearch(searchURL, params.Limit*2, 5)
	if err != nil {
		return nil, fmt.Errorf("error in initial job search: %v", err)
	}

	if len(allJobsOnPage) < params.Limit {
		log.Println("Not enough jobs found, modifying search parameters and retrying.")
		params = modifySearchParams(params)
		searchURL = buildLinkedinURL(params)
		additionalJobs, err := paginateJobSearch(searchURL, params.Limit*2-len(allJobsOnPage), 5)
		if err != nil {
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

	for _, job := range allJobsOnPage {
		if existingJobIDs[job.JobID] {
			log.Printf("Skipping duplicate job ID: %s", job.JobID)
			continue
		}

		wg.Add(1)
		go func(job JobInfo) {
			defer wg.Done()
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
		jobDetails = append(jobDetails, job)
		if len(jobDetails) >= params.Limit {
			break
		}
	}

	// Check for any errors
	var errors []string
	for err := range errorChan {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		log.Printf("Encountered %d errors while fetching job details", len(errors))
		// You can decide how to handle these errors. For now, we'll just log them.
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
			continue
		}

		descriptionElement := doc.Find("div.description__text")
		job.Description = strings.TrimSpace(descriptionElement.Text())
		job.DescriptionHTML, _ = descriptionElement.Html()

		job.CompanyLink = safeFindAttribute(doc, "a.topcard__org-name-link", "href")
		job.CompanyImgLink = safeFindAttribute(doc, "img.artdeco-entity-image", "data-delayed-url")
		job.SeniorityLevel = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Seniority level')"))
		job.EmploymentType = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Employment type') span.description__job-criteria-text"))
		job.JobFunction = strings.TrimSpace(safeFindString(doc, "li.description__job-criteria-item:contains('Job function')"))

		// Clean up job title and company name
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
		if experience, ok := filters["experience"].([]interface{}); ok {
			found := false
			for _, e := range experience {
				if e == "ENTRY_LEVEL" {
					found = true
					break
				}
			}
			if !found {
				experience = append(experience, "ENTRY_LEVEL")
			}
			filters["experience"] = experience
		}
	} else {
		params.Options["filters"] = map[string]interface{}{
			"type":       []string{"FULL_TIME"},
			"experience": []string{"ENTRY_LEVEL"},
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

		// Look for the specific code block containing job data
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

		// Check for the applyUrl code block
		applyURLRe := regexp.MustCompile(`<code id="applyUrl" style="display: none"><!--"(.*?)"--></code>`)
		applyURLMatch := applyURLRe.FindStringSubmatch(content)
		if len(applyURLMatch) > 1 {
			applyURL := html.UnescapeString(applyURLMatch[1])
			log.Printf("Found apply URL from code for %s: %s", fullJobURL, applyURL)
			return extractExternalURL(applyURL), nil
		}

		// Fallback methods if the primary method fails
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
		if err == nil {
			// Check for offsite apply button
			applyButton := doc.Find("a[data-tracking-control-name='public_jobs_apply-link-offsite']")
			if applyButton.Length() > 0 {
				applyURL, exists := applyButton.Attr("href")
				if exists {
					log.Printf("Found offsite apply button for %s: %s", fullJobURL, applyURL)
					return extractExternalURL(applyURL), nil
				}
			}

			// Check for alternative apply button
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

	// Check if this is a LinkedIn redirect URL
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

func scrapeLinkedinJobs(c *gin.Context) {
	var searchParams JobSearchParams
	if err := c.ShouldBindJSON(&searchParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Received request from %s", c.ClientIP())

	jobs, err := scrapeJobs(searchParams)
	if err != nil {
		log.Printf("Error scraping jobs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error scraping jobs"})
		return
	}

	if len(jobs) == 0 {
		log.Println("No jobs found matching the criteria")
		c.JSON(http.StatusNotFound, gin.H{"error": "No jobs found matching the criteria"})
		return
	}

	log.Printf("Total jobs fetched: %d", len(jobs))

	// Filter out existing job IDs
	existingJobIDs := make(map[string]bool)
	for _, id := range searchParams.ExistingJobIds {
		existingJobIDs[id] = true
	}
	filteredJobs := make([]JobInfo, 0)
	for _, job := range jobs {
		if !existingJobIDs[job.JobID] {
			filteredJobs = append(filteredJobs, job)
		}
	}
	log.Printf("Jobs after filtering existing IDs: %d", len(filteredJobs))

	// Apply time-based filter if specified
	if searchParams.MaxAgeDays != nil {
		currentTime := time.Now()
		timeFilteredJobs := make([]JobInfo, 0)
		for _, job := range filteredJobs {
			if job.Date != "" {
				jobDate, err := time.Parse(time.RFC3339, job.Date)
				if err == nil {
					if currentTime.Sub(jobDate).Hours()/24 <= float64(*searchParams.MaxAgeDays) {
						timeFilteredJobs = append(timeFilteredJobs, job)
					}
				}
			}
		}
		filteredJobs = timeFilteredJobs
		log.Printf("Jobs after applying time-based filter: %d", len(filteredJobs))
	}

	// Sort jobs by date (newest first) and limit to the requested number
	sort.Slice(filteredJobs, func(i, j int) bool {
		return filteredJobs[i].Date > filteredJobs[j].Date
	})
	if len(filteredJobs) > searchParams.Limit {
		filteredJobs = filteredJobs[:searchParams.Limit]
	}
	log.Printf("Jobs after sorting and limiting: %d", len(filteredJobs))

	// Fetch apply links for each job
	var wg sync.WaitGroup
	for i := range filteredJobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			applyLink, err := fetchApplyLink(filteredJobs[i].Link)
			if err != nil {
				log.Printf("Error fetching apply link for job %s: %v", filteredJobs[i].JobID, err)
			} else {
				filteredJobs[i].CompanyApplyURL = applyLink
				filteredJobs[i].ApplyLink = applyLink
			}
		}(i)
	}
	wg.Wait()

	log.Printf("Successfully scraped %d jobs", len(filteredJobs))
	c.JSON(http.StatusOK, filteredJobs)
}

func logRequestBody(c *gin.Context) {
	bodyBytes, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes)) // Restore the body for further use
	log.Printf("Received request from %s with body: %s", c.ClientIP(), string(bodyBytes))
	c.Next()
}

func main() {
	r := gin.Default()

	// Add CORS middleware
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}
	r.Use(cors.New(config))

	// Add request logging middleware
	r.Use(logRequestBody)

	// Set a longer timeout for the /scrape endpoint
	r.POST("/scrape", func(c *gin.Context) {
		timeout := time.Second * 60 // Set timeout to 60 seconds
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		scrapeLinkedinJobs(c)
	})

	// Get the port from the environment variable
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Default port if not specified
	}

	log.Printf("Starting server on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
