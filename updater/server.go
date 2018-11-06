package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/clientv3/concurrency"
	"github.com/cybozu-go/log"
	"github.com/cybozu-go/neco"
	"github.com/cybozu-go/neco/storage"
	"github.com/cybozu-go/well"
)

// Server represents neco-updater server
type Server struct {
	session *concurrency.Session
	storage storage.Storage
	timeout time.Duration

	checker ReleaseChecker
}

// NewServer returns a Server
func NewServer(session *concurrency.Session, storage storage.Storage, timeout time.Duration) Server {
	return Server{
		session: session,
		storage: storage,
		timeout: timeout,

		checker: NewReleaseChecker(storage),
	}
}

// Run runs neco-updater
func (s Server) Run(ctx context.Context) error {

	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	e := concurrency.NewElection(s.session, storage.KeyLeader)

RETRY:
	select {
	case <-s.session.Done():
		return errors.New("session has been orphaned")
	default:
	}

	err = e.Campaign(ctx, hostname)
	if err != nil {
		return err
	}
	leaderKey := e.Key()

	log.Info("I am the leader", map[string]interface{}{
		"session": s.session.Lease(),
	})

	env := well.NewEnvironment(ctx)
	env.Go(func(ctx context.Context) error {
		return s.runLoop(ctx, leaderKey)
	})
	env.Go(func(ctx context.Context) error {
		return s.checker.Run(ctx)
	})
	env.Stop()
	err = env.Wait()

	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	err2 := e.Resign(ctxWithTimeout)
	if err2 != nil {
		return err2
	}
	if err == storage.ErrNoLeader {
		log.Warn("lost the leadership", nil)
		goto RETRY
	}
	return err
}

func (s Server) runLoop(ctx context.Context, leaderKey string) error {
	var target string

	// Updater continues last update without create update reuqest with "skipRequest = true"
	var skipRequest bool

	req, err := s.storage.GetRequest(ctx)
	if err == nil {
		target = req.Version
		if req.Stop {
			log.Info("Last updating is failed, wait for retrying", map[string]interface{}{
				"version": req.Version,
			})
			err := s.waitRetry(ctx)
			if err != nil {
				return err
			}
		} else {
			log.Info("Last updating is still on progressing, wait for workers", map[string]interface{}{
				"version": req.Version,
			})
			skipRequest = true
		}
	} else if err != nil && err != storage.ErrNotFound {
		return err
	}

	for {
		if len(target) == 0 {
			target = s.checker.GetLatest()
		}
		if len(target) != 0 {
			// Found new update
			for {
				if !skipRequest {
					skipRequest = false
					err = s.startUpdate(ctx, target, leaderKey)
					if err == ErrNoMembers {
						break
					} else if err != nil {
						return err
					}
				}

				err = s.waitWorkers(ctx)
				if err == nil {
					break
				}
				if err != ErrUpdateFailed {
					return err
				}
				err := s.stopUpdate(ctx, leaderKey)
				if err != nil {
					return err
				}
				err = s.waitRetry(ctx)
				if err != nil {
					return err
				}
			}
		}

		err := s.waitForMemberUpdated(ctx)
		if err == context.DeadlineExceeded {
			target = ""
		} else if err != nil {
			return err
		}
	}
}

// startUpdate starts update with tag.  It returns ErrNoMembers if no
// bootservers are registered in etcd.
func (s Server) startUpdate(ctx context.Context, tag, leaderKey string) error {
	servers, err := s.storage.GetBootservers(ctx)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		log.Info("No bootservers exists in etcd", map[string]interface{}{})
		return ErrNoMembers
	}
	log.Info("Starting updating", map[string]interface{}{
		"version": tag,
		"servers": servers,
	})
	r := neco.UpdateRequest{
		Version:   tag,
		Servers:   servers,
		Stop:      false,
		StartedAt: time.Now(),
	}
	return s.storage.PutRequest(ctx, r, leaderKey)
}

func (s Server) stopUpdate(ctx context.Context, leaderKey string) error {
	req, err := s.storage.GetRequest(ctx)
	if err != nil {
		return err
	}
	req.Stop = true
	return s.storage.PutRequest(ctx, *req, leaderKey)
}

// waitWorkers waits for worker finishes updates until timed-out
func (s Server) waitWorkers(ctx context.Context) error {
	timeout, err := s.storage.GetWorkerTimeout(ctx)
	if err != nil {
		return err
	}

	req, rev, err := s.storage.GetRequestWithRev(ctx)
	if err != nil {
		return err
	}
	statuses := make(map[int]neco.UpdateStatus)

	deadline := req.StartedAt.Add(timeout)
	deadlineCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	ch := s.session.Client().Watch(
		deadlineCtx, storage.KeyStatusPrefix,
		clientv3.WithRev(rev+1), clientv3.WithFilterDelete(), clientv3.WithPrefix(),
	)
	for resp := range ch {
		for _, ev := range resp.Events {
			var st neco.UpdateStatus
			err = json.Unmarshal(ev.Kv.Value, &st)
			if err != nil {
				return err
			}
			lrn, err := strconv.Atoi(string(ev.Kv.Key[len(storage.KeyStatusPrefix):]))
			if err != nil {
				return err
			}
			if st.Version != req.Version {
				continue
			}
			statuses[lrn] = st

			if st.Error {
				log.Warn("worker failed updating", map[string]interface{}{
					"version": req.Version,
					"lrn":     lrn,
					"message": st.Message,
				})
				s.notifySlackServerFailure(ctx, *req, st)
				return ErrUpdateFailed
			}
			if st.Finished {
				log.Info("worker finished updating", map[string]interface{}{
					"version": req.Version,
					"lrn":     lrn,
				})
			}
		}

		success := true
		for _, lrn := range req.Servers {
			if st, ok := statuses[lrn]; !ok || !st.Finished || st.Version != req.Version {
				success = false
				break
			}
		}
		if success {
			log.Info("all worker finished updating", map[string]interface{}{
				"version": req.Version,
				"servers": req.Servers,
			})
			s.notifySlackSucceeded(ctx, *req)
			return nil
		}
	}

	log.Warn("workers timed-out", map[string]interface{}{
		"version":    req.Version,
		"started_at": req.StartedAt,
		"timeout":    timeout,
	})
	s.notifySlackTimeout(ctx, *req)
	return ErrUpdateFailed
}

func (s Server) waitRetry(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	_, rev, err := s.storage.GetRequestWithRev(ctx)
	if err == storage.ErrNotFound {
		return nil
	} else if err != nil {
		return err
	}

	ch := s.session.Client().Watch(ctx, storage.KeyCurrent, clientv3.WithRev(rev+1), clientv3.WithFilterPut())
	resp := <-ch
	if err := resp.Err(); err != nil {
		return err
	}
	return nil
}

// waitForMemberUpdated waits for new member added or member removed until with
// check-update-interval.  It returns nil error if member updated, or returns
// context.DeadlineExceeded if timed-out
func (s Server) waitForMemberUpdated(ctx context.Context) error {
	interval, err := s.storage.GetCheckUpdateInterval(ctx)
	if err != nil {
		return err
	}
	var lrns []int
	req, rev, err := s.storage.GetRequestWithRev(ctx)
	if err == nil {
		lrns = req.Servers
		sort.Ints(lrns)
	}
	if err != nil && err != storage.ErrNotFound {
		return err
	}

	withTimeoutCtx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()

	ch := s.session.Client().Watch(
		withTimeoutCtx, storage.KeyBootserversPrefix, clientv3.WithRev(rev+1),
	)

	var updated bool
	var lastErr error
	for resp := range ch {
		for _, ev := range resp.Events {
			lrn, err := strconv.Atoi(string(ev.Kv.Key[len(storage.KeyBootserversPrefix):]))
			if err != nil {
				lastErr = err
				cancel()
			}
			if ev.Type == clientv3.EventTypePut {
				if i := sort.SearchInts(lrns, lrn); i < len(lrns) && lrns[i] == lrn {
					continue
				}
			}
			updated = true
			cancel()
		}
	}
	if lastErr != nil {
		return lastErr
	}
	if !updated {
		return context.DeadlineExceeded
	}
	return nil
}

func (s Server) notifySlackSucceeded(ctx context.Context, req neco.UpdateRequest) error {
	slack, err := s.newSlackClient(ctx)
	if err == storage.ErrNotFound {
		return nil
	} else if err != nil {
		return err
	}

	att := Attachment{
		Color:      ColorGood,
		AuthorName: "Boot server updater",
		Title:      "Update completed successfully",
		Text:       "Updating on boot servers are completed successfully :tada: :tada: :tada:",
		Fields: []AttachmentField{
			{Title: "Version", Value: req.Version, Short: true},
			{Title: "Servers", Value: fmt.Sprintf("%v", req.Servers), Short: true},
			{Title: "Started at", Value: req.StartedAt.Format(time.RFC3339), Short: true},
		},
	}
	payload := Payload{Attachments: []Attachment{att}}
	return slack.PostWebHook(ctx, payload)
}

func (s Server) notifySlackServerFailure(ctx context.Context, req neco.UpdateRequest, st neco.UpdateStatus) error {
	slack, err := s.newSlackClient(ctx)
	if err == storage.ErrNotFound {
		return nil
	} else if err != nil {
		return err
	}

	att := Attachment{
		Color:      ColorDanger,
		AuthorName: "Boot server updater",
		Title:      "Failed to update boot servers",
		Text:       "Failed to update boot servers due to some worker return(s) error :crying_cat_face:.  Please fix it manually.",
		Fields: []AttachmentField{
			{Title: "Version", Value: "1.0.0", Short: true},
			{Title: "Servers", Value: fmt.Sprintf("%v", req.Servers), Short: true},
			{Title: "Started at", Value: req.StartedAt.Format(time.RFC3339), Short: true},
			{Title: "Reason", Value: st.Message, Short: true},
		},
	}
	payload := Payload{Attachments: []Attachment{att}}
	return slack.PostWebHook(ctx, payload)
}

func (s Server) notifySlackTimeout(ctx context.Context, req neco.UpdateRequest) error {
	slack, err := s.newSlackClient(ctx)
	if err == storage.ErrNotFound {
		return nil
	} else if err != nil {
		return err
	}

	att := Attachment{
		Color:      ColorDanger,
		AuthorName: "Boot server updater",
		Title:      "Update failed on the boot servers",
		Text:       "Failed to update boot servers due to timed-out from worker updates :crying_cat_face:.  Please fix it manually.",
		Fields: []AttachmentField{
			{Title: "Version", Value: "1.0.0", Short: true},
			{Title: "Servers", Value: fmt.Sprintf("%v", req.Servers), Short: true},
			{Title: "Started at", Value: req.StartedAt.Format(time.RFC3339), Short: true},
		},
	}
	payload := Payload{Attachments: []Attachment{att}}
	return slack.PostWebHook(ctx, payload)
}

func (s Server) newSlackClient(ctx context.Context) (*SlackClient, error) {
	webhookURL, err := s.storage.GetSlackNotification(ctx)
	if err == storage.ErrNotFound {
		return nil, storage.ErrNotFound
	} else if err != nil {
		return nil, err
	}

	var http *http.Client

	proxyURL, err := s.storage.GetProxyConfig(ctx)
	if err == storage.ErrNotFound {
	} else if err != nil {
		return nil, err
	} else {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		http = neco.NewHTTP(u)
	}

	return &SlackClient{URL: webhookURL, HTTP: http}, nil
}
