package raft

import (
	"errors"
	"fmt"
	"time"
)

type State uint8

const (
	follower State = iota
	candidate
	leader
)

var States = [...]string{
	"follower",
	"candidate",
	"leader",
	"dead",
}

func (s *RaftServer) becomeFollower(term int32) {
	s.state = follower
	s.currentTerm = term
	s.votedFor = ""
	s.lastHeartbeat = time.Now()
	fmt.Println("becomeFollower: running election timer")
	go s.runElectionTimer()
}

func (s *RaftServer) becomeCandidate() error {
	if s.state == leader {
		return errors.New("current state is 'leader', leaders cannot become candidates")
	} else {
		s.state = candidate
	}
	return nil
}

func (s *RaftServer) becomeLeader() {
	if s.state != candidate {
		s.errChan <- fmt.Errorf("only candidates can become leaders, current state is '%v'", s.state)
		return
	} else {
		s.state = leader
	}

	for _, peer := range s.quorum {
		s.nextIndex[peer.Id] = len(s.log)
		s.matchIndex[peer.Id] = -1
	}

	go s.sendHeartbeatsAsLeader()
}
