package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache"
	bolt "go.etcd.io/bbolt"
)

// URL struct for our data model - keeps it simple
type URL struct {
	ID          int       `json:"id"`
	OriginalURL string    `json:"original_url"`
	ShortCode   string    `json:"short_code"`
	CreatedAt   time.Time `json:"created_at"`
	ClickCount  int       `json:"click_count"`
}

// response structs for api endpoints
type ShortenResponse struct {
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
	ShortCode   string `json:"short_code"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// main app struct - holds db connection and cache
type App struct {
	DB    *bolt.DB
	Cache *cache.Cache // in-memory cache for hot urls - way faster than hitting db everytime
}

// base62 chars for encoding - same approach tinyurl uses
const base62Chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateShortCode creates unique short codes using md5 hash + base62 encoding
// this approach prevents collisions better than just random strings
func (app *App) generateShortCode(originalURL string) (string, error) {
	// create hash from url + timestamp to ensure uniquness
	hasher := md5.New()
	hasher.Write([]byte(originalURL + fmt.Sprintf("%d", time.Now().UnixNano())))
	hash := hex.EncodeToString(hasher.Sum(nil))
	
	// convert first 8 chars of hash to base62 - gives us good distribution
	shortCode := ""
	for i := 0; i < 8; i++ {
		if i < len(hash) {
			charIndex := int(hash[i]) % 62
			shortCode += string(base62Chars[charIndex])
		}
	}
	
	// double check if this code already exists (very unlikely but safety first)
	exists := false
	err := app.DB.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("urls"))
		if bucket != nil {
			v := bucket.Get([]byte(shortCode))
			if v != nil {
				exists = true
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	
	// if somehow we got collision, try again with different timestamp
	if exists {
		time.Sleep(time.Nanosecond) // tiny delay to change timestamp
		return app.generateShortCode(originalURL)
	}
	
	return shortCode, nil
}

// validates if url is properly formatted - basic but effective
func isValidURL(str string) bool {
	u, err := url.Parse(str)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// handles POST /api/shorten - main endpoint for creating short urls
func (app *App) shortenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// parse json request
	var req struct {
		URL string `json:"url"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "invalid json"})
		return
	}
	
	// add http if missing - user friendly feature
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		req.URL = "https://" + req.URL
	}
	
	// validate the url format
	if !isValidURL(req.URL) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "invalid url format"})
		return
	}
	
	// check if we already have this url shortened - avoid duplicates
	var existingCode string
	err := app.DB.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("reverse"))
		if bucket != nil {
			v := bucket.Get([]byte(req.URL))
			if v != nil {
				existingCode = string(v)
			}
		}
		return nil
	})
	if err == nil && existingCode != "" {
		// found existing, return it instead of creating new one
		shortURL := fmt.Sprintf("http://localhost:8080/%s", existingCode)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ShortenResponse{
			ShortURL:    shortURL,
			OriginalURL: req.URL,
			ShortCode:   existingCode,
		})
		return
	}
	
	// generate new short code
	shortCode, err := app.generateShortCode(req.URL)
	if err != nil {
		log.Printf("error generating short code: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "server error"})
		return
	}
	
	// save to database 
	err = app.DB.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("urls"))
		if err != nil {
			return err
		}
		
		// store url data as json
		urlData := URL{
			OriginalURL: req.URL,
			ShortCode:   shortCode,
			CreatedAt:   time.Now(),
			ClickCount:  0,
		}
		
		urlJSON, err := json.Marshal(urlData)
		if err != nil {
			return err
		}
		
		// store short code -> url data
		err = bucket.Put([]byte(shortCode), urlJSON)
		if err != nil {
			return err
		}
		
		// also store reverse mapping for duplicate detection
		reverseBucket, err := tx.CreateBucketIfNotExists([]byte("reverse"))
		if err != nil {
			return err
		}
		
		return reverseBucket.Put([]byte(req.URL), []byte(shortCode))
	})
	
	if err != nil {
		log.Printf("database insert error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "failed to save url"})
		return
	}
	
	// cache the new url for fast access later
	app.Cache.Set(shortCode, req.URL, cache.DefaultExpiration)
	
	// return success response
	shortURL := fmt.Sprintf("http://localhost:8080/%s", shortCode)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ShortenResponse{
		ShortURL:    shortURL,
		OriginalURL: req.URL,
		ShortCode:   shortCode,
	})
}

// handles GET /{shortCode} - redirects to original url
func (app *App) redirectHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	shortCode := vars["shortCode"]
	
	if shortCode == "" {
		http.NotFound(w, r)
		return
	}
	
	// try cache first - much faster than db lookup
	if originalURL, found := app.Cache.Get(shortCode); found {
		// increment click counter in background - dont make user wait
		go func() {
			app.DB.Update(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("urls"))
				if bucket != nil {
					v := bucket.Get([]byte(shortCode))
					if v != nil {
						var urlData URL
						if json.Unmarshal(v, &urlData) == nil {
							urlData.ClickCount++
							if updatedJSON, err := json.Marshal(urlData); err == nil {
								bucket.Put([]byte(shortCode), updatedJSON)
							}
						}
					}
				}
				return nil
			})
		}()
		
		http.Redirect(w, r, originalURL.(string), http.StatusMovedPermanently)
		return
	}
	
	// not in cache, check database
	var originalURL string
	err := app.DB.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("urls"))
		if bucket != nil {
			v := bucket.Get([]byte(shortCode))
			if v != nil {
				var urlData URL
				if json.Unmarshal(v, &urlData) == nil {
					originalURL = urlData.OriginalURL
				}
			}
		}
		return nil
	})
	
	if err != nil || originalURL == "" {
		http.NotFound(w, r)
		return
	}
	
	// add to cache for next time
	app.Cache.Set(shortCode, originalURL, cache.DefaultExpiration)
	
	// increment click counter in background
	go func() {
		app.DB.Update(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("urls"))
			if bucket != nil {
				v := bucket.Get([]byte(shortCode))
				if v != nil {
					var urlData URL
					if json.Unmarshal(v, &urlData) == nil {
						urlData.ClickCount++
						if updatedJSON, err := json.Marshal(urlData); err == nil {
							bucket.Put([]byte(shortCode), updatedJSON)
						}
					}
				}
			}
			return nil
		})
	}()
	
	http.Redirect(w, r, originalURL, http.StatusMovedPermanently)
}

// serves the main html page
func (app *App) indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <title>LinkFast</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { 
            font-family: Arial, sans-serif;
            background: #f5f5f5;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .container {
            background: white;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
            width: 100%;
            max-width: 400px;
        }
        h1 {
            text-align: center;
            color: #333;
            margin-bottom: 20px;
            font-size: 24px;
        }
        input {
            width: 100%;
            padding: 12px;
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 16px;
            margin-bottom: 15px;
        }
        button {
            width: 100%;
            padding: 12px;
            background: #007bff;
            color: white;
            border: none;
            border-radius: 4px;
            font-size: 16px;
            cursor: pointer;
        }
        button:hover {
            background: #0056b3;
        }
        .result {
            margin-top: 15px;
            padding: 15px;
            background: #f8f9fa;
            border-radius: 4px;
            display: none;
        }
        .result.show { display: block; }
        .short-url {
            color: #007bff;
            font-weight: bold;
            word-break: break-all;
        }
        .error {
            color: #dc3545;
            margin-top: 10px;
        }
        .copy-btn {
            margin-top: 10px;
            padding: 8px 16px;
            background: #28a745;
            font-size: 14px;
            width: auto;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>LinkFast</h1>
        <input type="url" id="urlInput" placeholder="Enter URL to shorten">
        <button onclick="shortenUrl()">Shorten</button>
        <div id="result" class="result">
            <p>Short URL: <span class="short-url" id="shortUrl"></span></p>
            <button class="copy-btn" onclick="copyToClipboard()">Copy</button>
        </div>
        <div id="error" class="error"></div>
    </div>

    <script>
        async function shortenUrl() {
            const url = document.getElementById('urlInput').value;
            const errorDiv = document.getElementById('error');
            const resultDiv = document.getElementById('result');
            
            errorDiv.textContent = '';
            resultDiv.classList.remove('show');
            
            if (!url) {
                errorDiv.textContent = 'Please enter a URL';
                return;
            }
            
            try {
                const response = await fetch('/api/shorten', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ url: url })
                });
                
                const data = await response.json();
                
                if (response.ok) {
                    document.getElementById('shortUrl').textContent = data.short_url;
                    resultDiv.classList.add('show');
                } else {
                    errorDiv.textContent = data.error || 'Error occurred';
                }
            } catch (error) {
                errorDiv.textContent = 'Network error';
            }
        }
        
        function copyToClipboard() {
            const shortUrl = document.getElementById('shortUrl').textContent;
            navigator.clipboard.writeText(shortUrl).then(() => {
                const btn = document.querySelector('.copy-btn');
                btn.textContent = 'Copied!';
                setTimeout(() => btn.textContent = 'Copy', 2000);
            });
        }
        
        document.getElementById('urlInput').addEventListener('keypress', function(e) {
            if (e.key === 'Enter') shortenUrl();
        });
    </script>
</body>
</html>`
	
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, tmpl)
}

// database setup function
func setupDatabase(db *bolt.DB) error {
	// create buckets (like tables)
	return db.Update(func(tx *bolt.Tx) error {
		// bucket for short code -> url data
		_, err := tx.CreateBucketIfNotExists([]byte("urls"))
		if err != nil {
			return err
		}
		
		// bucket for reverse mapping (original url -> short code)
		_, err = tx.CreateBucketIfNotExists([]byte("reverse"))
		return err
	})
}

func main() {
	// use boltdb for embedded database - runs entirely in your go process
	dbPath := "urls.db"
	
	// connect to boltdb database (creates file if doesn't exist)
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal("failed to connect to database:", err)
	}
	defer db.Close()
	
	// setup database buckets
	if err := setupDatabase(db); err != nil {
		log.Fatal("failed to setup database:", err)
	}
	
	// create cache with 5 minute default expiration, cleanup every 10 minutes
	// this will keep hot urls super fast to access
	cache := cache.New(5*time.Minute, 10*time.Minute)
	
	// create app instance
	app := &App{
		DB:    db,
		Cache: cache,
	}
	
	// setup routes
	r := mux.NewRouter()
	r.HandleFunc("/", app.indexHandler).Methods("GET")
	r.HandleFunc("/api/shorten", app.shortenHandler).Methods("POST")
	r.HandleFunc("/{shortCode:[a-zA-Z0-9]{8}}", app.redirectHandler).Methods("GET")
	
	// get port from environment or default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	
	log.Printf("server starting on port %s", port)
	log.Printf("visit http://localhost:%s to use the url shortener", port)
	
	// start server with timeouts for production readiness
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	
	log.Fatal(srv.ListenAndServe())
}