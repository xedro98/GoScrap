# LinkedIn Job Scraper

## Overview

This project is a LinkedIn job scraper built with Go. It allows users to search for jobs on LinkedIn based on various parameters and returns detailed job information. The scraper uses the Gin web framework for handling HTTP requests and the GoQuery library for parsing HTML.

## Features

- **Job Search**: Search for jobs on LinkedIn using keywords, locations, and other filters.
- **Job Details**: Fetch detailed information about each job, including title, company, location, description, and more.
- **Proxy Rotation**: Use a list of proxies to avoid IP blocking.
- **CORS Support**: Configured to allow cross-origin requests.
- **Request Logging**: Logs incoming requests for debugging purposes.
- **Concurrency**: Uses goroutines to fetch apply links concurrently for better performance.

## Installation

1. Clone the repository:
    ```sh
    git clone https://github.com/yourusername/linkedin-job-scraper.git
    cd linkedin-job-scraper
    ```

2. Install dependencies:
    ```sh
    go mod download
    ```

3. Run the server:
    ```sh
    go run main.go
    ```

## Usage

### API Endpoint

- **POST /scrape**

  **Request Body:**
  ```json
  {
    "query": "Software Engineer",
    "locations": ["San Francisco, CA", "New York, NY"],
    "limit": 10,
    "options": {
      "filters": {
        "type": ["Full-time", "Part-time"]
      }
    },
    "existingJobIds": ["12345", "67890"],
    "max_age_days": 7
  }
  ```

  **Response:**
  ```json
  [
    {
      "jobId": "123456",
      "title": "Software Engineer",
      "company": "Tech Company",
      "place": "San Francisco, CA",
      "link": "https://www.linkedin.com/jobs/view/123456",
      "description": "Job description here...",
      "descriptionHTML": "<div>Job description here...</div>",
      "applyLink": "https://www.linkedin.com/jobs/apply/123456",
      "companyApplyUrl": "https://www.linkedin.com/jobs/apply/123456"
    }
  ]
  ```

## Code Reference

- **Main Function**: Initializes the server and sets up routes and middleware.
  
```200:310:main.go
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
```


- **Job Scraping Logic**: Handles the job scraping logic, including filtering and sorting jobs.
  
```372:389:main.go
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
```


## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request for any changes.

## Contact

For any questions or inquiries, please contact [me@xed.ro](mailto:me@xed.ro).
