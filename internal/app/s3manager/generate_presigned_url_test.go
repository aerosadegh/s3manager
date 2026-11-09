package s3manager_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudlena/s3manager/internal/app/s3manager"
	"github.com/cloudlena/s3manager/internal/app/s3manager/mocks"
	"github.com/gorilla/mux"
	"github.com/matryer/is"
)

func TestHandleGenerateURL(t *testing.T) {
	t.Parallel()

	presignedURL, _ := url.Parse("https://s3.example.com/bucket/object?X-Amz-Signature=abc123")

	cases := []struct {
		it                     string
		presignedGetObjectFunc func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error)
		expiry                 string
		expectedStatusCode     int
		expectedBodyContains   string
	}{
		{
			it: "generates a presigned URL",
			presignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return presignedURL, nil
			},
			expiry:               "3600",
			expectedStatusCode:   http.StatusOK,
			expectedBodyContains: "s3.example.com",
		},
		{
			it: "returns error for invalid expiry",
			presignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return nil, nil
			},
			expiry:               "not-a-number",
			expectedStatusCode:   http.StatusInternalServerError,
			expectedBodyContains: "error converting expiry",
		},
		{
			it: "returns error when expiry is zero",
			presignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return nil, nil
			},
			expiry:               "0",
			expectedStatusCode:   http.StatusInternalServerError,
			expectedBodyContains: "invalid expiry value",
		},
		{
			it: "returns error when expiry exceeds 7 days",
			presignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return nil, nil
			},
			expiry:               "604801",
			expectedStatusCode:   http.StatusInternalServerError,
			expectedBodyContains: "invalid expiry value",
		},
		{
			it: "returns error if there is an S3 error",
			presignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return nil, errS3
			},
			expiry:               "3600",
			expectedStatusCode:   http.StatusInternalServerError,
			expectedBodyContains: "mocked s3 error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.it, func(t *testing.T) {
			t.Parallel()
			is := is.New(t)

			s3 := &mocks.S3Mock{
				PresignedGetObjectFunc: tc.presignedGetObjectFunc,
			}
			instances := s3manager.S3Instances{{ID: "1", Name: "primary", Client: s3, Presigner: s3}}

			r := mux.NewRouter()
			r.Handle("/{instance}/api/buckets/{bucketName}/objects/{objectName}/url", s3manager.HandleGenerateURL(instances)).Methods(http.MethodGet)

			ts := httptest.NewServer(r)
			defer ts.Close()

			resp, err := http.Get(fmt.Sprintf("%s/primary/api/buckets/my-bucket/objects/my-object/url?expiry=%s", ts.URL, tc.expiry))
			is.NoErr(err)
			defer func() {
				err = resp.Body.Close()
				is.NoErr(err)
			}()
			body, err := io.ReadAll(resp.Body)
			is.NoErr(err)

			is.Equal(tc.expectedStatusCode, resp.StatusCode)
			is.True(strings.Contains(string(body), tc.expectedBodyContains))
		})
	}
}

func TestHandleGenerateURLUsesPresigner(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	presignedURL, _ := url.Parse("https://public.example.com/bucket/object?X-Amz-Signature=abc123")
	instances := s3manager.S3Instances{{
		ID:     "1",
		Name:   "primary",
		Client: &mocks.S3Mock{},
		Presigner: &mocks.S3Mock{
			PresignedGetObjectFunc: func(context.Context, string, string, time.Duration, url.Values) (*url.URL, error) {
				return presignedURL, nil
			},
		},
	}}

	r := mux.NewRouter()
	r.Handle("/{instance}/api/buckets/{bucketName}/objects/{objectName}/url", s3manager.HandleGenerateURL(instances)).Methods(http.MethodGet)

	req := httptest.NewRequest(http.MethodGet, "/primary/api/buckets/my-bucket/objects/my-object/url?expiry=3600", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	is.Equal(http.StatusOK, rr.Code)
	is.True(strings.Contains(rr.Body.String(), "public.example.com"))
}
