package desiredstatefetcher

import (
	"errors"
	"fmt"
	"github.com/cloudfoundry/go_cfmessagebus/fake_cfmessagebus"
	"github.com/cloudfoundry/hm9000/config"
	"github.com/cloudfoundry/hm9000/models"
	"github.com/cloudfoundry/hm9000/test_helpers/app"
	"github.com/cloudfoundry/hm9000/test_helpers/fake_bel_air"
	"github.com/cloudfoundry/hm9000/test_helpers/fake_http_client"
	"github.com/cloudfoundry/hm9000/test_helpers/fake_time_provider"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"net/http"
)

type brokenReader struct{}

func (b *brokenReader) Read([]byte) (int, error) {
	return 0, errors.New("oh no you didn't!")
}

func (b *brokenReader) Close() error {
	return nil
}

var _ = Describe("DesiredStateFetcher", func() {
	var (
		fakeMessageBus *fake_cfmessagebus.FakeMessageBus
		fetcher        *desiredStateFetcher
		httpClient     *fake_http_client.FakeHttpClient
		timeProvider   *fake_time_provider.FakeTimeProvider
		freshPrince    *fake_bel_air.FakeFreshPrince
		resultChan     chan DesiredStateFetcherResult
	)

	BeforeEach(func() {
		resultChan = make(chan DesiredStateFetcherResult, 1)
		timeProvider = &fake_time_provider.FakeTimeProvider{
			TimeToProvide: time.Now(),
		}

		fakeMessageBus = fake_cfmessagebus.NewFakeMessageBus()
		httpClient = fake_http_client.NewFakeHttpClient()
		freshPrince = &fake_bel_air.FakeFreshPrince{}

		fetcher = NewDesiredStateFetcher(fakeMessageBus, etcdStore, httpClient, freshPrince, timeProvider, desiredStateServerBaseUrl, config.DESIRED_STATE_POLLING_BATCH_SIZE)
		fetcher.Fetch(resultChan)
	})

	Describe("Authentication", func() {
		It("should request the CC credentials over NATS", func() {
			Ω(fakeMessageBus.Requests).Should(HaveKey(authNatsSubject))
			Ω(fakeMessageBus.Requests[authNatsSubject]).Should(HaveLen(1))
			Ω(fakeMessageBus.Requests[authNatsSubject][0].Message).Should(BeEmpty())
		})

		Context("when we've received the authentication information", func() {
			var request *fake_http_client.Request

			BeforeEach(func() {
				fakeMessageBus.Requests[authNatsSubject][0].Callback([]byte(`{"user":"mcat","password":"testing"}`))
				request = httpClient.LastRequest()
			})

			It("should make the correct request", func() {
				Ω(httpClient.Requests).Should(HaveLen(1))

				Ω(request.URL.String()).Should(ContainSubstring(desiredStateServerBaseUrl))
				Ω(request.URL.Path).Should(ContainSubstring("/bulk/apps"))

				expectedAuth := models.BasicAuthInfo{
					User:     "mcat",
					Password: "testing",
				}.Encode()

				Ω(request.Header.Get("Authorization")).Should(Equal(expectedAuth))
			})
		})

		Context("when the authentication information was corrupted", func() {
			BeforeEach(func() {
				fakeMessageBus.Requests[authNatsSubject][0].Callback([]byte(`{`))
			})

			It("should not make any requests", func() {
				Ω(httpClient.Requests).Should(HaveLen(0))
			})

			It("should send an error down the result channel", func(done Done) {
				result := <-resultChan
				Ω(result.Success).Should(BeFalse())
				Ω(result.Message).Should(Equal("Failed to parse authentication info from JSON"))
				Ω(result.Error).Should(HaveOccured())
				close(done)
			}, 0.1)
		})

		Context("when the authentication information fails to arrive", func() {
			It("should not make any requests", func() {
				Ω(httpClient.Requests).Should(HaveLen(0))
			})
		})
	})

	Describe("Fetching with an invalid URL", func() {
		BeforeEach(func() {
			fetcher = NewDesiredStateFetcher(fakeMessageBus, etcdStore, httpClient, freshPrince, timeProvider, "http://example.com/#%ZZ", config.DESIRED_STATE_POLLING_BATCH_SIZE)
			fetcher.Fetch(resultChan)

			fakeMessageBus.Requests[authNatsSubject][1].Callback([]byte(`{"user":"mcat","password":"testing"}`))
		})

		It("should send an error down the result channel", func(done Done) {
			result := <-resultChan
			Ω(result.Success).Should(BeFalse())
			Ω(result.Message).Should(Equal("Failed to generate URL request"))
			Ω(result.Error).Should(HaveOccured())
			close(done)
		}, 0.1)
	})

	Describe("Fetching batches", func() {
		var response desiredStateServerResponse

		BeforeEach(func() {
			fakeMessageBus.Requests[authNatsSubject][0].Callback([]byte(`{"user":"mcat","password":"testing"}`))
		})

		It("should request a batch size with an empty bulk token", func() {
			query := httpClient.LastRequest().URL.Query()
			Ω(query.Get("batch_size")).Should(Equal(fmt.Sprintf("%d", config.DESIRED_STATE_POLLING_BATCH_SIZE)))
			Ω(query.Get("bulk_token")).Should(Equal("{}"))
		})

		Context("when a response with desired state is received", func() {
			var (
				a1 app.App
				a2 app.App
			)

			BeforeEach(func() {
				a1 = app.NewApp()
				a2 = app.NewApp()

				response = desiredStateServerResponse{
					Results: map[string]models.DesiredAppState{
						a1.AppGuid: a1.DesiredState(0),
						a2.AppGuid: a2.DesiredState(0),
					},
					BulkToken: bulkToken{
						Id: 5,
					},
				}

				httpClient.LastRequest().Succeed(response.ToJson())
			})

			It("should store the desired states", func() {
				node, err := etcdStore.Get("/desired/" + a1.AppGuid + "-" + a1.AppVersion)
				Ω(err).ShouldNot(HaveOccured())

				Ω(node.TTL).Should(BeNumerically("==", config.DESIRED_STATE_TTL-1))

				Ω(node.Value).Should(Equal(a1.DesiredState(0).ToJson()))

				node, err = etcdStore.Get("/desired/" + a2.AppGuid + "-" + a2.AppVersion)
				Ω(err).ShouldNot(HaveOccured())

				Ω(node.TTL).Should(BeNumerically("==", config.DESIRED_STATE_TTL-1))

				Ω(node.Value).Should(Equal(a2.DesiredState(0).ToJson()))
			})

			It("should request the next batch", func() {
				Ω(httpClient.Requests).Should(HaveLen(2))
				Ω(httpClient.LastRequest().URL.Query().Get("bulk_token")).Should(Equal(response.BulkTokenRepresentation()))
			})

			It("should not bump the freshness yet", func() {
				Ω(freshPrince.Key).Should(BeZero())
			})

			It("should not send a result down the resultChan yet", func() {
				Ω(resultChan).Should(HaveLen(0))
			})
		})

		Context("when an empty response is received", func() {
			BeforeEach(func() {
				response = desiredStateServerResponse{
					Results: map[string]models.DesiredAppState{},
					BulkToken: bulkToken{
						Id: 17,
					},
				}

				httpClient.LastRequest().Succeed(response.ToJson())
			})

			It("should stop requesting batches", func() {
				Ω(httpClient.Requests).Should(HaveLen(1))
			})

			It("should bump the freshness", func() {
				Ω(freshPrince.Key).Should(Equal(config.DESIRED_FRESHNESS_KEY))
				Ω(freshPrince.Timestamp).Should(Equal(timeProvider.Time()))
				Ω(freshPrince.TTL).Should(BeNumerically("==", config.DESIRED_FRESHNESS_TTL))
			})

			It("should send a succesful result down the result channel", func(done Done) {
				result := <-resultChan
				Ω(result.Success).Should(BeTrue())
				Ω(result.Message).Should(BeZero())
				Ω(result.Error).ShouldNot(HaveOccured())
				close(done)
			}, 0.1)
		})

		assertFailure := func(expectedMessage string) {
			It("should stop requesting batches", func() {
				Ω(httpClient.Requests).Should(HaveLen(1))
			})

			It("should not bump the freshness", func() {
				Ω(freshPrince.Key).Should(BeZero())
			})

			It("should send an error down the result channel", func(done Done) {
				result := <-resultChan
				Ω(result.Success).Should(BeFalse())
				Ω(result.Message).Should(Equal(expectedMessage))
				Ω(result.Error).Should(HaveOccured())
				close(done)
			}, 1.0)
		}

		Context("when an unauthorized response is received", func() {
			BeforeEach(func() {
				httpClient.LastRequest().RespondWithStatus(http.StatusUnauthorized)
			})

			assertFailure("HTTP request received unauthorized response code")
		})

		Context("when the HTTP request returns a non-200 response", func() {
			BeforeEach(func() {
				httpClient.LastRequest().RespondWithStatus(http.StatusNotFound)
			})

			assertFailure("HTTP request received non-200 response (404)")
		})

		Context("when the HTTP request fails with an error", func() {
			BeforeEach(func() {
				httpClient.LastRequest().RespondWithError(errors.New(":("))
			})

			assertFailure("HTTP request failed with error")
		})

		Context("when a broken body is received", func() {
			BeforeEach(func() {
				response := &http.Response{
					Status:     "StatusOK (200)",
					StatusCode: http.StatusOK,

					ContentLength: 17,
					Body:          &brokenReader{},
				}

				httpClient.LastRequest().Callback(response, nil)
			})

			assertFailure("Failed to read HTTP response body")
		})

		Context("when a malformed response is received", func() {
			BeforeEach(func() {
				httpClient.LastRequest().Succeed([]byte("ß"))
			})

			assertFailure("Failed to parse HTTP response body JSON")
		})

		Context("when it fails to write to the store", func() {
			BeforeEach(func() {
				a := app.NewApp()
				etcdStore.Set("/desired/"+a.AppGuid+"-"+a.AppVersion+"/foo", []byte("mwahahaha"), 10)

				response = desiredStateServerResponse{
					Results: map[string]models.DesiredAppState{
						a.AppGuid: a.DesiredState(0),
					},
					BulkToken: bulkToken{
						Id: 5,
					},
				}

				httpClient.LastRequest().Succeed(response.ToJson())
			})

			assertFailure("Failed to store desired state in store")
		})
	})
})