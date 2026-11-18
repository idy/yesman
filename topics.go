package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// topicsEvent names a yesman whose topics changed.
var topicsEvent = mygo.NewEvent[string]("topics")

func (a *app) topicsChanged(yesman string) {
	if a.onTopics != nil {
		a.onTopics(yesman)
		return
	}
	_ = topicsEvent.Broadcast(yesman)
}

// Topics returns a yesman's topics. since is its hiring time as the page
// knows it, so a page of a dismissed yesman cannot read or write a new one
// hired with the same name.
func (s *service) Topics(yesman string, since time.Time) ([]yesmen.Topic, error) {
	return s.app.yesmen.Topics(yesman, since)
}

// CreateTopic adds a topic the user wrote.
func (s *service) CreateTopic(yesman string, since time.Time, in yesmen.TopicInput) (TopicUpdate, error) {
	t, added, err := s.app.yesmen.CreateTopicSources(yesman, since, yesmen.TopicByUser, in)
	if err != nil {
		return TopicUpdate{}, err
	}
	s.app.topicsChanged(yesman)
	return TopicUpdate{Topic: t, Sources: added}, nil
}

// TopicUpdate is the result of a user's change: the topic as saved, or, on
// Conflict, as it is now, with nothing saved. Sources holds, for each source
// the change added, the ID it has in the topic, so a form edited while it
// was saving knows which saved source each of its rows is.
type TopicUpdate struct {
	Topic    yesmen.Topic `json:"topic"`
	Conflict bool         `json:"conflict,omitempty"`
	Sources  []string     `json:"sources,omitempty"`
}

// UpdateTopic saves the user's change if the topic is still at the revision
// they edited.
func (s *service) UpdateTopic(yesman string, since time.Time, id string, p yesmen.TopicPatch) (TopicUpdate, error) {
	t, added, err := s.app.yesmen.PatchTopicSources(yesman, since, yesmen.TopicByUser, id, p)
	var conflict *yesmen.TopicConflictError
	if errors.As(err, &conflict) {
		return TopicUpdate{Topic: conflict.Current, Conflict: true}, nil
	}
	if err != nil {
		return TopicUpdate{}, err
	}
	s.app.topicsChanged(yesman)
	return TopicUpdate{Topic: t, Sources: added}, nil
}

// TopicSubmission is a message sent from a topic: its receipt from the
// lead's ordinary submission path, and the topic's record of it.
type TopicSubmission struct {
	Delivery agent.Delivery   `json:"delivery"`
	Send     yesmen.TopicSend `json:"send"`
	Text     string           `json:"text"`
}

// SubmitTopic sends the user's message about a topic to the yesman's lead,
// through the same receipt and queue as its composer, into its one
// conversation. The topic records the submission ID before dispatch; the
// message carries a visible line naming the topic, frozen at the first
// attempt so an unchanged retry is the same submission.
func (s *service) SubmitTopic(ctx context.Context, yesman string, since time.Time, id, messageID, text string, images []agent.Image) (TopicSubmission, error) {
	if s.app.piErr != nil {
		return TopicSubmission{}, s.app.piErr
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return TopicSubmission{}, errors.New("empty message")
	}
	if err := agent.ValidateImages(images); err != nil {
		return TopicSubmission{}, err
	}
	send, err := s.app.yesmen.RegisterTopicSend(yesman, since, id, yesmen.TopicSend{ID: messageID, ImageCount: len(images), AgentSince: since}, text)
	if err != nil {
		return TopicSubmission{}, err
	}
	s.app.topicsChanged(yesman)
	full := yesmen.TopicMessage(send.Context, text)
	// The topic is also the message's trusted conversation context: host-only,
	// so pi never lets it share an occurrence with the composer's or another
	// topic's input. Its text and pi's command stay exactly as sent.
	d, err := s.app.agents.SubmitInContext(ctx, agent.ID(yesman, yesmen.LeadName), messageID, full, images, topicContext(id), since)
	state := d.State
	if err != nil {
		state = "failed"
		if ctx.Err() != nil {
			state = "unconfirmed"
		}
	}
	if state != "" && state != send.State {
		// A note for the page; the receipt itself stays the lead's.
		if serr := s.app.yesmen.SetTopicSendState(yesman, since, id, messageID, state); serr == nil {
			send.State = state
			s.app.topicsChanged(yesman)
		}
	}
	if err != nil {
		return TopicSubmission{Send: send, Text: full}, err
	}
	return TopicSubmission{Delivery: d, Send: send, Text: full}, nil
}

// topicContext is the host-only conversation context of a topic's messages
// (agent.SubmitInContext); topic IDs such as tp3 fit its bounds.
func topicContext(id string) string { return "topic:" + id }
