// Command seed fills a MinIO bucket with objects whose Last Modified times are
// spread over the past three years, so that sorting and paging can be tried on
// realistic data. The test stack in compose.test.yaml runs it.
//
// S3 stamps every object with the time it was uploaded. MinIO also accepts the
// time an object was modified elsewhere in X-Minio-Source-Mtime (minio-go:
// Internal.SourceMTime), which its replication relies on, so this only works
// against MinIO.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// kinds are the types of file seeded, picked at random for each object.
var kinds = []struct{ prefix, ext, contentType string }{
	{"invoice", "csv", "text/csv"},
	{"report", "md", "text/markdown"},
	{"server", "log", "text/plain"},
	{"export", "json", "application/json"},
	{"notes", "txt", "text/plain"},
}

func main() {
	endpoint := getenv("SEED_ENDPOINT", "localhost:9000")
	bucket := getenv("SEED_BUCKET", "archive")
	count, err := strconv.Atoi(getenv("SEED_COUNT", "250"))
	if err != nil || count < 1 {
		log.Fatalf("invalid SEED_COUNT %q", os.Getenv("SEED_COUNT"))
	}

	ctx := context.Background()
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(getenv("SEED_ACCESS_KEY_ID", "s3manager"), getenv("SEED_SECRET_ACCESS_KEY", "s3manager"), ""),
		Region: "us-east-1",
	})
	if err != nil {
		log.Fatal(err)
	}

	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
			log.Fatal(err)
		}
	}

	// A fixed seed makes every run create the same objects, so running it
	// again overwrites them instead of adding more. Their times count back
	// from today.
	r := rand.New(rand.NewPCG(42, 2026))
	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(-3, 0, 0)

	var oldest, newest time.Time
	for i := 1; i <= count; i++ {
		kind := kinds[r.IntN(len(kinds))]
		name := fmt.Sprintf("%s-%03d.%s", kind.prefix, i, kind.ext)
		mtime := start.Add(time.Duration(r.Int64N(int64(end.Sub(start))))).Truncate(time.Second)
		size := int(200 * math.Exp(r.Float64()*math.Log(150_000.0/200.0))) // 200 B to 150 KB, mostly small

		body := makeBody(kind.ext, name, mtime, size)
		_, err := client.PutObject(ctx, bucket, name, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{
			ContentType: kind.contentType,
			Internal:    minio.AdvancedPutOptions{SourceMTime: mtime},
		})
		if err != nil {
			log.Fatalf("error putting %s: %v", name, err)
		}

		if oldest.IsZero() || mtime.Before(oldest) {
			oldest = mtime
		}
		if mtime.After(newest) {
			newest = mtime
		}
	}

	log.Printf("seeded %d objects into %s, last modified %s to %s", count, bucket, oldest.Format(time.DateOnly), newest.Format(time.DateOnly))
}

// getenv returns the environment variable key, or fallback if it is unset.
func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	return fallback
}

// makeBody builds plausible content of about size bytes for the file type.
func makeBody(ext, name string, mtime time.Time, size int) []byte {
	var b bytes.Buffer
	stamp := mtime.Format(time.RFC3339)

	switch ext {
	case "csv":
		b.WriteString("line,item,quantity,unit_price\n")
		for i := 1; b.Len() < size; i++ {
			fmt.Fprintf(&b, "%d,item-%d,%d,%d.%02d\n", i, i*7%997, i%13+1, i%90+5, i%100)
		}
	case "json":
		fmt.Fprintf(&b, "{\n  \"file\": %q,\n  \"created\": %q,\n  \"records\": [\n", name, stamp)
		for i := 1; b.Len() < size; i++ {
			fmt.Fprintf(&b, "    {\"id\": %d, \"value\": %d},\n", i, i*31%1000)
		}
		b.WriteString("    {\"id\": 0, \"value\": 0}\n  ]\n}\n")
	case "log":
		for i := 0; b.Len() < size; i++ {
			fmt.Fprintf(&b, "%s INFO request %d served in %dms\n", mtime.Add(time.Duration(i)*time.Second).Format(time.RFC3339), i, i*17%400)
		}
	case "md":
		fmt.Fprintf(&b, "# %s\n\nCreated %s.\n\n", name, stamp)
		for i := 1; b.Len() < size; i++ {
			fmt.Fprintf(&b, "- Finding %d: everything within expected range.\n", i)
		}
	default:
		fmt.Fprintf(&b, "%s, written %s\n\n", name, stamp)
		for b.Len() < size {
			b.WriteString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.\n")
		}
	}

	return b.Bytes()
}
