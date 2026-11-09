package s3manager

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

// maxPresignedURLExpiry is the longest lifetime S3 accepts for a presigned URL.
const maxPresignedURLExpiry = 7 * 24 * time.Hour

// HandleGenerateURL generates a presigned URL. It is signed by the instance's
// Presigner, which may address S3 differently than the app itself does.
func HandleGenerateURL(instances S3Instances) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instance, ok := resolveInstance(w, r, instances)
		if !ok {
			return
		}

		bucketName := mux.Vars(r)["bucketName"]
		objectName := mux.Vars(r)["objectName"]

		seconds, err := strconv.ParseInt(r.URL.Query().Get("expiry"), 10, 64)
		if err != nil {
			handleHTTPError(w, fmt.Errorf("error converting expiry: %w", err))
			return
		}

		if seconds < 1 || seconds > int64(maxPresignedURLExpiry/time.Second) {
			handleHTTPError(w, fmt.Errorf("invalid expiry value: %v", seconds))
			return
		}

		expiry := time.Duration(seconds) * time.Second
		presignedURL, err := instance.Presigner.PresignedGetObject(r.Context(), bucketName, objectName, expiry, url.Values{})
		if err != nil {
			handleHTTPError(w, fmt.Errorf("error generating url: %w", err))
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"url": presignedURL.String()})
	}
}
