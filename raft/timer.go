package raft

import (
	"fmt"
	"math/rand"
	"time"
)

type Ticker struct {
	period  time.Duration
	timeout *time.Ticker
	ch      <-chan time.Time
}

func NewTicker(period time.Duration) *Ticker {
	timeout := *time.NewTicker(period)
	return &Ticker{
		period:  period,
		timeout: &timeout,
		ch:      timeout.C,
	}
}

func (s *RaftServer) runElectionTimer() {
	s.Lock()
	startingTerm := s.currentTerm
	s.Unlock()

	timeout := getElectionTimeout()
	ticker := NewTicker(200 * time.Millisecond)
	defer ticker.timeout.Stop()

	for {
		<-ticker.ch

		s.Lock()
		if s.state == leader {
			fmt.Printf("election timer running while in leader state")
			s.Unlock()
			return
		}

		if startingTerm != s.currentTerm {
			fmt.Printf("term incremented to %v while election timer running, expecting %v\n", s.currentTerm, startingTerm)
			s.Unlock()
			return
		}

		if elapsed := time.Since(s.lastHeartbeat); elapsed >= timeout {
			s.startElection()
			s.Unlock()
			return
		}
		s.Unlock()
	}
}

func getElectionTimeout() time.Duration {
	timeout := rand.Intn(3000)
	randomTimeout := time.Duration(3000+timeout) * time.Millisecond
	return randomTimeout
}

func (t *Ticker) Reset(period time.Duration) {
	t.timeout.Reset(period)
}

func (s *RaftServer) sendHeartbeatsAsLeader() {
	heartbeatTimeout := 1000 * time.Millisecond
	timer := time.NewTimer(heartbeatTimeout)
	defer timer.Stop()

	println("Sending heartbeats as leader")
	for {
		doSend := false
		fmt.Printf("sending heartbeats at %v\n", time.Now())
		<-timer.C
		doSend = true
		timer.Stop()
		timer.Reset(heartbeatTimeout)

		if doSend {
			s.Lock()
			if s.state != leader {
				println("State no longer leader, stopping heartbeats")
				s.Unlock()
				return
			}
			s.Unlock()
			s.sendHeartbeat()
		}
	}
}
