package s3manager

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/s3utils"
)

// S3InstanceConfig holds the configuration of a single S3 instance.
type S3InstanceConfig struct {
	Name                string
	Endpoint            string
	UseIam              bool
	IamEndpoint         string
	AccessKeyID         string
	SecretAccessKey     string
	Region              string
	UseSSL              bool
	SkipSSLVerification bool
	SignatureType       string
	BucketLookup        string
	PublicURL           string
	// PresignURL is the address presigned links are signed for, such as
	// https://s3.example.com, when it differs from Endpoint. Requires Region.
	PresignURL string
	// Buckets are listed on top of the ones the instance reports itself, so
	// that buckets owned by someone else, like public datasets, can be browsed.
	Buckets []string
}

// S3Instance is a configured S3 instance and its client.
type S3Instance struct {
	ID     string
	Name   string
	Client S3
	// Presigner signs the time-limited download links. It is Client unless
	// the links must point to a different address than the app talks to.
	Presigner S3
	// BucketLookup is how the client addresses buckets, which public links
	// follow unless PublicURL overrides them.
	BucketLookup minio.BucketLookupType
	// PublicURL is an optional template for public object links, such as a
	// CDN in front of the bucket. See PublicObjectURL.
	PublicURL string
	// Buckets are the configured bucket names that are always listed, even
	// when the instance does not report them or refuses to list its buckets.
	Buckets []string
}

// S3Instances is the ordered set of configured S3 instances. It is built once
// at start-up and never modified, so handlers can read it concurrently.
type S3Instances []*S3Instance

// signatureTypes maps the SIGNATURE_TYPE configuration value to its S3 equivalent.
var signatureTypes = map[string]credentials.SignatureType{
	"V2":          credentials.SignatureV2,
	"V4":          credentials.SignatureV4,
	"V4Streaming": credentials.SignatureV4Streaming,
	"Anonymous":   credentials.SignatureAnonymous,
}

// bucketLookups maps the BUCKET_LOOKUP configuration value to its S3
// equivalent. It decides whether a bucket is addressed in the path
// (endpoint/bucket) or in the host name (bucket.endpoint); `Auto` lets
// minio-go pick, which means host-name style for Amazon and Google endpoints
// and path style everywhere else.
var bucketLookups = map[string]minio.BucketLookupType{
	"Auto": minio.BucketLookupAuto,
	"DNS":  minio.BucketLookupDNS,
	"Path": minio.BucketLookupPath,
}

// NewS3Instances creates a client for every given configuration. Instances are
// addressable by their name and by their position in the list, starting at 1.
func NewS3Instances(configs []S3InstanceConfig) (S3Instances, error) {
	if len(configs) == 0 {
		return nil, errors.New("no S3 instances configured")
	}

	instances := make(S3Instances, 0, len(configs))
	for i, config := range configs {
		bucketLookup, ok := bucketLookups[config.BucketLookup]
		if config.BucketLookup != "" && !ok {
			return nil, fmt.Errorf("invalid BUCKET_LOOKUP: %s", config.BucketLookup)
		}
		if config.PublicURL != "" && !strings.Contains(config.PublicURL, "{key}") {
			return nil, fmt.Errorf("PUBLIC_URL of instance %s must contain the {key} placeholder", config.Name)
		}
		for _, bucket := range config.Buckets {
			if err := s3utils.CheckValidBucketName(bucket); err != nil {
				return nil, fmt.Errorf("invalid bucket %q in BUCKETS of instance %s: %w", bucket, config.Name, err)
			}
		}

		creds, err := newCredentials(config)
		if err != nil {
			return nil, err
		}
		client, err := newS3Client(config, bucketLookup, creds)
		if err != nil {
			return nil, err
		}
		presigner, err := newPresigner(config, bucketLookup, creds, client)
		if err != nil {
			return nil, err
		}
		instances = append(instances, &S3Instance{
			ID:           strconv.Itoa(i + 1),
			Name:         config.Name,
			Client:       client,
			Presigner:    presigner,
			BucketLookup: bucketLookup,
			PublicURL:    config.PublicURL,
			Buckets:      config.Buckets,
		})
	}

	return instances, nil
}

// newCredentials creates the credentials described by a single instance
// configuration. They are shared by all of the instance's clients, so that IAM
// credentials are fetched only once.
func newCredentials(config S3InstanceConfig) (*credentials.Credentials, error) {
	if config.UseIam {
		return credentials.NewIAM(config.IamEndpoint), nil
	}

	signatureType, ok := signatureTypes[config.SignatureType]
	if !ok {
		return nil, fmt.Errorf("invalid SIGNATURE_TYPE: %s", config.SignatureType)
	}

	return credentials.NewStatic(config.AccessKeyID, config.SecretAccessKey, "", signatureType), nil
}

// newS3Client creates the S3 client described by a single instance configuration.
func newS3Client(config S3InstanceConfig, bucketLookup minio.BucketLookupType, creds *credentials.Credentials) (*minio.Client, error) {
	opts := &minio.Options{
		Creds:        creds,
		Secure:       config.UseSSL,
		Region:       config.Region,
		BucketLookup: bucketLookup,
	}

	if config.UseSSL && config.SkipSSLVerification {
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	client, err := minio.New(config.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("error creating s3 client for instance %s: %w", config.Name, err)
	}

	return client, nil
}

// Get returns the instance with the given ID or name.
func (instances S3Instances) Get(identifier string) (*S3Instance, error) {
	for _, instance := range instances {
		if instance.ID == identifier {
			return instance, nil
		}
	}
	for _, instance := range instances {
		if instance.Name == identifier {
			return instance, nil
		}
	}

	return nil, fmt.Errorf("S3 instance '%s' not found", identifier)
}

// WithInstance resolves the instance addressed by the request's {instance}
// variable and delegates to a handler for that instance's client.
func WithInstance(instances S3Instances, handler func(S3) http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instance, ok := resolveInstance(w, r, instances)
		if !ok {
			return
		}

		handler(instance.Client)(w, r)
	}
}

// resolveInstance looks up the instance addressed by the request's {instance}
// variable, responding with a not found error if there is no such instance.
func resolveInstance(w http.ResponseWriter, r *http.Request, instances S3Instances) (*S3Instance, bool) {
	instance, err := instances.Get(mux.Vars(r)["instance"])
	if err != nil {
		http.Error(w, fmt.Sprintf("Instance not found: %s", err), http.StatusNotFound)
		return nil, false
	}

	return instance, true
}

// newPresigner returns the client that signs an instance's download links:
// its own client, unless PresignURL names a different address. Presigning
// happens locally, apart from looking up a bucket's region, which is why a
// separate address requires a region: otherwise that lookup would go to an
// address the app may not even be able to reach.
func newPresigner(config S3InstanceConfig, bucketLookup minio.BucketLookupType, creds *credentials.Credentials, client *minio.Client) (S3, error) {
	if config.PresignURL == "" {
		return client, nil
	}

	presignURL, err := url.Parse(config.PresignURL)
	if err != nil {
		return nil, fmt.Errorf("invalid PRESIGN_URL of instance %s: %w", config.Name, err)
	}
	if presignURL.Scheme != "http" && presignURL.Scheme != "https" || presignURL.Host == "" ||
		strings.Trim(presignURL.Path, "/") != "" || presignURL.RawQuery != "" || presignURL.Fragment != "" || presignURL.User != nil {
		return nil, fmt.Errorf("PRESIGN_URL of instance %s must be an http or https URL without a path, such as https://s3.example.com", config.Name)
	}
	if config.Region == "" {
		return nil, fmt.Errorf("PRESIGN_URL of instance %s requires REGION to be set", config.Name)
	}

	presigner, err := minio.New(presignURL.Host, &minio.Options{
		Creds:        creds,
		Secure:       presignURL.Scheme == "https",
		Region:       config.Region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating presigning client for instance %s: %w", config.Name, err)
	}

	return presigner, nil
}
