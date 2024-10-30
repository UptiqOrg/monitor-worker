package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

type Request struct {
	Region string `json:"region"`
	Urls   []URL  `json:"urls"`
}

type URL struct {
	WebsiteID uuid.UUID `json:"websiteId"`
	URL       string    `json:"url"`
}

type Result struct {
	WebsiteID   uuid.UUID `json:"websiteId"`
	URL         string    `json:"url"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"statusCode"`
	ResponseTime int64    `json:"responseTime"`
}

var db *sql.DB

func loadEnv() error {
	log.Print("Loading environment variables")
	if err := godotenv.Load(".env"); err != nil {
		return err
	}
	return nil
}

func init() {
	if err := loadEnv(); err != nil {
		log.Print("Error loading environment variables from .env")
	}

	var err error
	dbConnString := os.Getenv("SECRET_XATA_PG_ENDPOINT")
	db, err = sql.Open("postgres", dbConnString)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to connect to database")
	}

	if err = db.Ping(); err != nil {
		log.Fatal().Err(err).Msg("Unable to ping database")
	}
}

func pingURL(url URL, wg *sync.WaitGroup, results chan<- Result) {
	defer wg.Done()

	start := time.Now()
	resp, err := http.Get(url.URL)
	responseTime := time.Since(start).Milliseconds()

	result := Result{
		WebsiteID:   url.WebsiteID,
		URL:         url.URL,
		ResponseTime: responseTime,
	}

	if err != nil {
		result.Status = "down"
		result.StatusCode = 0
	} else {
		defer resp.Body.Close()
		result.StatusCode = resp.StatusCode
		if responseTime > 1000 {
			result.Status = "degraded"
		} else {
			result.Status = "up"
		}
	}

	results <- result
}

func insertResult(result Result) error {
	_, err := db.Exec(
		`INSERT INTO uptime_checks (website_id, status, response_time, status_code)
		VALUES ($1, $2, $3, $4)`,
		result.WebsiteID, result.Status, result.ResponseTime, result.StatusCode)
	return err
}

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	apiKey := r.Header.Get("X-API-Key")
	expectedApiKey := os.Getenv("API_KEY")
	if apiKey == "" || apiKey != expectedApiKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Urls) > 5 {
		http.Error(w, "Too many URLs, maximum allowed is 5", http.StatusBadRequest)
		return
	}

	var wg sync.WaitGroup
	results := make(chan Result, len(req.Urls))

	for _, url := range req.Urls {
		wg.Add(1)
		go pingURL(url, &wg, results)
	}

	wg.Wait()
	close(results)

	var resultList []Result
	for result := range results {
		resultList = append(resultList, result)
		log.Printf("WebsiteID: %s, URL: %s, Status: %s, StatusCode: %d, ResponseTime: %dms",
			result.WebsiteID, result.URL, result.Status, result.StatusCode, result.ResponseTime)

		if err := insertResult(result); err != nil {
			log.Error().Err(err).Msg("Error inserting result into database")
		}
	}

	response, err := json.Marshal(resultList)
	if err != nil {
		http.Error(w, "Error generating response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(response)
}
