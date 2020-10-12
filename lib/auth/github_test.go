/*
Copyright 2017 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"context"
	"fmt"
	"net/url"
	"time"

	authority "github.com/gravitational/teleport/lib/auth/testauthority"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/backend/lite"
	"github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/trace"

	"github.com/jonboulle/clockwork"
	"gopkg.in/check.v1"
)

type GithubSuite struct {
	a           *AuthServer
	mockEmitter *events.MockEmitter
	b           backend.Backend
	c           clockwork.FakeClock
}

var _ = fmt.Printf
var _ = check.Suite(&GithubSuite{})

func (s *GithubSuite) SetUpSuite(c *check.C) {
	var err error

	utils.InitLoggerForTests()

	s.c = clockwork.NewFakeClockAt(time.Now())

	s.b, err = lite.NewWithConfig(context.Background(), lite.Config{
		Path:             c.MkDir(),
		PollStreamPeriod: 200 * time.Millisecond,
		Clock:            s.c,
	})
	c.Assert(err, check.IsNil)

	clusterName, err := services.NewClusterName(services.ClusterNameSpecV2{
		ClusterName: "me.localhost",
	})
	c.Assert(err, check.IsNil)

	authConfig := &InitConfig{
		ClusterName:            clusterName,
		Backend:                s.b,
		Authority:              authority.New(),
		SkipPeriodicOperations: true,
	}
	s.a, err = NewAuthServer(authConfig)
	c.Assert(err, check.IsNil)

	s.mockEmitter = &events.MockEmitter{}
	s.a.emitter = s.mockEmitter
}

func (s *GithubSuite) TestPopulateClaims(c *check.C) {
	claims, err := populateGithubClaims(&testGithubAPIClient{})
	c.Assert(err, check.IsNil)
	c.Assert(claims, check.DeepEquals, &services.GithubClaims{
		Username: "octocat",
		OrganizationToTeams: map[string][]string{
			"org1": {"team1", "team2"},
			"org2": {"team1"},
		},
	})
}

func (s *GithubSuite) TestCreateGithubUser(c *check.C) {
	// Create GitHub user with 1 minute expiry.
	_, err := s.a.createGithubUser(&createUserParams{
		connectorName: "github",
		username:      "foo",
		logins:        []string{"foo"},
		roles:         []string{"admin"},
		sessionTTL:    1 * time.Minute,
	})
	c.Assert(err, check.IsNil)

	// Within that 1 minute period the user should still exist.
	_, err = s.a.GetUser("foo", false)
	c.Assert(err, check.IsNil)

	// Advance time 2 minutes, the user should be gone.
	s.c.Advance(2 * time.Minute)
	_, err = s.a.GetUser("foo", false)
	c.Assert(err, check.NotNil)
}

type testGithubAPIClient struct{}

func (c *testGithubAPIClient) getUser() (*userResponse, error) {
	return &userResponse{Login: "octocat"}, nil
}

func (c *testGithubAPIClient) getTeams() ([]teamResponse, error) {
	return []teamResponse{
		{
			Name: "team1",
			Slug: "team1",
			Org:  orgResponse{Login: "org1"},
		},
		{
			Name: "team2",
			Slug: "team2",
			Org:  orgResponse{Login: "org1"},
		},
		{
			Name: "team1",
			Slug: "team1",
			Org:  orgResponse{Login: "org2"},
		},
	}, nil
}

func (s *GithubSuite) TestValidateGithubAuthCallbackEventsEmitted(c *check.C) {
	auth := &githubAuthResponse{
		auth: GithubAuthResponse{
			Username: "test-name",
		},
		claims: map[string][]string{
			"test": {},
		},
	}

	m := &mockedGithubManager{}

	// Test success event.
	m.mockValidateGithubAuthCallback = func(q url.Values) (*githubAuthResponse, error) {
		return auth, nil
	}
	_, _ = validateGithubAuthCallbackHelper(context.Background(), m, nil, s.a.emitter)
	c.Assert(s.mockEmitter.LastEvent().GetType(), check.Equals, events.UserLoginEvent)
	c.Assert(s.mockEmitter.LastEvent().GetCode(), check.Equals, events.UserSSOLoginCode)
	s.mockEmitter.Reset()

	// Test failure event.
	m.mockValidateGithubAuthCallback = func(q url.Values) (*githubAuthResponse, error) {
		return auth, trace.BadParameter("")
	}
	_, _ = validateGithubAuthCallbackHelper(context.Background(), m, nil, s.a.emitter)
	c.Assert(s.mockEmitter.LastEvent().GetCode(), check.Equals, events.UserSSOLoginFailureCode)
}

type mockedGithubManager struct {
	mockValidateGithubAuthCallback func(q url.Values) (*githubAuthResponse, error)
}

func (m *mockedGithubManager) validateGithubAuthCallback(q url.Values) (*githubAuthResponse, error) {
	if m.mockValidateGithubAuthCallback != nil {
		return m.mockValidateGithubAuthCallback(q)
	}

	return nil, trace.NotImplemented("mockValidateGithubAuthCallback not implemented")
}
