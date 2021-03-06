package mcat_test

import (
	"github.com/cloudfoundry/hm9000/models"
	"github.com/cloudfoundry/hm9000/testhelpers/appfixture"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Crashes", func() {
	var (
		dea               appfixture.DeaFixture
		a                 appfixture.AppFixture
		crashingHeartbeat models.Heartbeat
	)

	BeforeEach(func() {
		dea = appfixture.NewDeaFixture()
		a = dea.GetApp(0)
	})

	Describe("when all instances are crashed", func() {
		BeforeEach(func() {
			simulator.SetDesiredState(a.DesiredState(3))

			crashingHeartbeat = dea.HeartbeatWith(
				a.CrashedInstanceHeartbeatAtIndex(0),
				a.CrashedInstanceHeartbeatAtIndex(1),
				a.CrashedInstanceHeartbeatAtIndex(2),
			)

			simulator.SetCurrentHeartbeats(crashingHeartbeat)
			simulator.Tick(simulator.TicksToAttainFreshness)
		})

		It("should only try to start instance at index 0", func() {
			Ω(startStopListener.StartCount()).Should(Equal(1))
			startMsg := startStopListener.Start(0)
			Ω(startMsg.AppVersion).Should(Equal(a.AppVersion))
			Ω(startMsg.InstanceIndex).Should(Equal(0))
		})

		It("should never try to stop crashes", func() {
			Ω(startStopListener.StopCount()).Should(BeZero())
			simulator.Tick(1)
			Ω(startStopListener.StopCount()).Should(BeZero())
		})
	})

	Describe("when at least one instance is running", func() {
		BeforeEach(func() {
			simulator.SetDesiredState(a.DesiredState(3))

			crashingHeartbeat = dea.HeartbeatWith(
				a.CrashedInstanceHeartbeatAtIndex(0),
				a.InstanceAtIndex(1).Heartbeat(),
				a.CrashedInstanceHeartbeatAtIndex(2),
			)

			simulator.SetCurrentHeartbeats(crashingHeartbeat)
			simulator.Tick(simulator.TicksToAttainFreshness)
		})

		It("should start all the crashed instances", func() {
			Ω(startStopListener.StopCount()).Should(BeZero())
			Ω(startStopListener.StartCount()).Should(Equal(2))

			indicesToStart := []int{
				startStopListener.Start(0).InstanceIndex,
				startStopListener.Start(1).InstanceIndex,
			}

			Ω(indicesToStart).Should(ContainElement(0))
			Ω(indicesToStart).Should(ContainElement(2))
		})
	})

	Describe("the backoff policy", func() {
		BeforeEach(func() {
			simulator.SetDesiredState(a.DesiredState(2))

			crashingHeartbeat = dea.HeartbeatWith(
				a.InstanceAtIndex(0).Heartbeat(),
				a.CrashedInstanceHeartbeatAtIndex(1),
			)

			simulator.SetCurrentHeartbeats(crashingHeartbeat)
			simulator.Tick(simulator.TicksToAttainFreshness)
		})

		Context("when the app keeps crashing", func() {
			It("should keep restarting the app instance with an appropriate backoff", func() {
				//crash #2
				simulator.Tick(simulator.GracePeriod)
				startStopListener.Reset()
				simulator.Tick(1)
				Ω(startStopListener.StartCount()).Should(Equal(1))

				//crash #3
				simulator.Tick(simulator.GracePeriod)
				startStopListener.Reset()
				simulator.Tick(1)
				Ω(startStopListener.StartCount()).Should(Equal(1))

				//crash #4, backoff begins
				simulator.Tick(simulator.GracePeriod)
				startStopListener.Reset()
				simulator.Tick(1)
				Ω(startStopListener.StartCount()).Should(BeZero())

				//take more ticks longer to send a new start messages
				simulator.Tick(simulator.GracePeriod)
				Ω(startStopListener.StartCount()).Should(Equal(1))
			})
		})

		Context("when the app starts running", func() {
			BeforeEach(func() {
				//crash #2
				simulator.Tick(simulator.GracePeriod) //wait for keep-alive to expire
				simulator.Tick(1)                     //sends start for #2

				//crash #3
				simulator.Tick(simulator.GracePeriod) //wait for keep-alive #2 to expire
				simulator.Tick(1)                     //sends start for #3

				simulator.Tick(simulator.GracePeriod) //wait for keep-alive #3 to expire
				runningHeartbeat := dea.HeartbeatWith(
					a.InstanceAtIndex(0).Heartbeat(),
					a.InstanceAtIndex(1).Heartbeat(),
					a.CrashedInstanceHeartbeatAtIndex(1),
				)

				startStopListener.Reset()
				simulator.SetCurrentHeartbeats(runningHeartbeat)
				simulator.Tick(1) //app is running, no starts should be scheduled
				Ω(startStopListener.StartCount()).Should(BeZero())
			})

			Context("when it starts crashing again *before* the crash count expires", func() {
				It("should continue the backoff policy where it left off", func() {
					simulator.SetCurrentHeartbeats(crashingHeartbeat)
					simulator.Tick(1) //running heartbeat is gone; schedule a start from where the policy left off
					Ω(startStopListener.StartCount()).Should(BeZero())
					simulator.Tick(simulator.GracePeriod)
					Ω(startStopListener.StartCount()).Should(Equal(1))
				})
			})

			Context("when it starts crashing again *after* the crash count expires", func() {
				It("should reset the backoff policy", func() {
					simulator.Tick(6 * 2) //6 is the maximum backoff (cli_runner_test sets this in the config) and the crash count TTL is max backoff * 2
					simulator.SetCurrentHeartbeats(crashingHeartbeat)
					simulator.Tick(1) //schedule and send a start immediately
					Ω(startStopListener.StartCount()).Should(Equal(1))
				})
			})
		})
	})
})
