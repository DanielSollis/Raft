package raft

import (
	"Raft/api"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog/log"
)

// Invoked by leader to replicate log entries (section 5.3 of the Raft whitepaper);
// also used as heartbeat (section 5.2 of the Raft whitepaper).
//
// Receiver implementation:
//  1. Reply false if term < currentTerm (section §5.1 of the Raft whitepaper).
//  2. Reply false if log doesn’t contain an entry at prevLogIndex
//     whose term matches prevLogTerm (section §5.3 of the Raft whitepaper).
//  3. If an existing entry conflicts with a new one (same index
//     but different terms), delete the existing entry and all that
//     follow it (section §5.3 of the Raft whitepaper).
//  4. Append any new entries not already in the log.
//  5. If leaderCommit > commitIndex, set commitIndex =
//     min(leaderCommit, index of last new entry).
func (s *RaftServer) AppendEntries(stream api.Raft_AppendEntriesServer) (err error) {
	//fmt.Println("s.locking 1")
	s.Lock()
	//defer fmt.Println("s.unlocking 1")
	defer s.Unlock()

	var req *api.AppendEntriesRequest

	if req, err = stream.Recv(); err != nil {
		return err
	}

	rep := &api.AppendEntriesReply{Id: s.id, Term: s.currentTerm, Success: false}

	// because there cannot be two leaders at a time, if append entries is called on a leader
	// we can expect that this was initiated by a client.
	if req.Term == s.currentTerm {
		if s.state != follower {
			fmt.Printf("AppendEntries: s.state != follower, reverting to follower")
			s.becomeFollower(req.Term)
		}
		fmt.Printf("AppendEntries: received heartbeat from %v at %v\n", req.LeaderId, time.Now())
		s.lastHeartbeat = time.Now()

		if req.PrevLogIndex == -1 || (req.PrevLogIndex < int32(len(s.log)) && req.PrevLogTerm == s.log[req.PrevLogIndex].Term) {
			rep.Success = true

			insertIndex := req.PrevLogIndex + 1
			newEntryIndex := 0

			for {
				if (insertIndex >= int32(len(s.log)) || newEntryIndex >= len(req.Entries)) || s.log[insertIndex].Term != req.Entries[newEntryIndex].Term {
					break
				}
				insertIndex++
				newEntryIndex++
			}

			if newEntryIndex < len(req.Entries) {
				s.log = append(s.log[:insertIndex], req.Entries[newEntryIndex:]...)
			}

			if req.LeaderCommit > s.commitIndex {
				s.commitIndex = int32(len(s.log) - 1)
				if req.LeaderCommit < int32(len(s.log)-1) {
					s.commitIndex = req.LeaderCommit
				}
				s.newCommitReady <- struct{}{}
			}
		}
	}

	rep.Term = s.currentTerm
	stream.SendAndClose(rep)
	fmt.Printf("AppendEntries: current log: %v\n", s.log)
	return nil
}

// Invoked by candidates to gather votes (§5.2).
//
// Receiver implementation:
//  1. Reply false if term < currentTerm (§5.1).
//  2. If votedFor is null or candidateId, and candidate’s log is at
//     least as up-to-date as receiver’s log, grant vote (sections §5.2, §5.4 of the
//     Raft whitepaper).
func (s *RaftServer) RequestVote(ctx context.Context, in *api.VoteRequest) (out *api.VoteReply, err error) {
	//fmt.Println("s.locking 2")
	s.Lock()
	//defer fmt.Println("s.unlocking 2")
	defer s.Unlock()
	lastLogIndex, lastLogTerm := s.lastLogIndexAndTerm()
	out = &api.VoteReply{Id: s.id, Term: s.currentTerm}

	if in.Term > s.currentTerm {
		fmt.Printf("RequestVote: in.Term > s.currentTerm, reverting to follower\n")
		s.becomeFollower(in.Term)
	}

	if s.currentTerm == in.Term &&
		(s.votedFor == in.CandidateId || s.votedFor == "") &&
		(in.LastLogTerm > lastLogTerm || (in.LastLogTerm == lastLogTerm &&
			in.LastLogIndex >= lastLogIndex)) {
		fmt.Printf("granting vote to %v\n", in.CandidateId)
		out.VoteGranted = true
		s.votedFor = in.CandidateId
		s.lastHeartbeat = time.Now()
	} else {
		out.VoteGranted = false
	}
	out.Term = s.currentTerm
	return out, nil
}

func (s *RaftServer) startElection() {
	var err error
	if err = s.becomeCandidate(); err != nil {
		s.errChan <- err
		return
	}

	s.currentTerm += 1
	startingTerm := s.currentTerm
	s.lastHeartbeat = time.Now()

	// vote for self
	s.votedFor = s.id
	fmt.Printf("starting election, incremented term to %v\n", s.currentTerm)

	votesReceived := 1

	var out *api.VoteReply
	for _, peer := range s.quorum {
		go func(peer Peer) {
			//fmt.Println("s.locking 3")
			s.Lock()
			savedLastLogIndex, savesLastLogTerm := s.lastLogIndexAndTerm()
			//fmt.Println("s.unlocking 3")
			s.Unlock()

			in := &api.VoteRequest{
				Term:         startingTerm,
				CandidateId:  s.id,
				LastLogIndex: savedLastLogIndex,
				LastLogTerm:  savesLastLogTerm,
			}

			fmt.Printf("requesting vote from %s\n", peer.Id)
			if out, err = peer.Client.RequestVote(context.Background(), in); err != nil {
				s.errChan <- err
				return
			}
			fmt.Printf("%s\n", out)
			//fmt.Println("s.locking 4")
			s.Lock()
			//fmt.Println("s.unlocking 4")
			defer s.Unlock()

			if s.state != candidate {
				fmt.Printf("state changed to %v, abandoning election\n", States[s.state])
				return
			}

			if out.Term > startingTerm {
				fmt.Printf("startElection: %v > startingTerm: %v, reverting to follower\n", out.Term, startingTerm)
				s.becomeFollower(out.Term)
				return
			} else if out.Term == startingTerm {
				if out.VoteGranted {
					votesReceived += 1
					if votesReceived*2 > len(s.quorum)+1 {
						fmt.Println("startElection: Election successful, becoming leader")
						s.becomeLeader()
						return
					}
				}
			}
		}(peer)
	}
	fmt.Println("startElection: running election timer")
	go s.runElectionTimer()
}

func (s *RaftServer) lastLogIndexAndTerm() (int64, int32) {
	if len(s.log) > 0 {
		lastIndex := len(s.log) - 1
		return int64(lastIndex), s.log[lastIndex].Term
	} else {
		return -1, -1
	}
}

func (s *RaftServer) sendHeartbeat() {
	//fmt.Println("s.locking 5")
	s.Lock()
	if s.state != leader {
		s.Unlock()
		return
	}
	startingTerm := s.currentTerm
	fmt.Printf("sendHeartbeat: current log: %v\n", s.log)
	//fmt.Println("s.unlocking 5")
	s.Unlock()

	var err error
	var out *api.AppendEntriesReply
	for _, peer := range s.quorum {
		var stream api.Raft_AppendEntriesClient
		if stream, err = peer.Client.AppendEntries(context.TODO()); err != nil {
			s.errChan <- err
			return
		}

		go func(peer Peer) {
			//fmt.Println("s.locking 6")
			s.Lock()
			nextIndex := s.nextIndex[peer.Id]
			prevLogIndex := nextIndex - 1
			prevLogTerm := -1
			if prevLogIndex >= 0 {
				prevLogTerm = int(s.log[prevLogIndex].Term)
			}
			entries := s.log[nextIndex:]

			in := &api.AppendEntriesRequest{
				Id:           s.id,
				Term:         startingTerm,
				LeaderId:     s.id,
				PrevLogIndex: int32(prevLogIndex),
				PrevLogTerm:  int32(prevLogTerm),
				Entries:      entries,
				LeaderCommit: s.commitIndex,
			}
			//fmt.Println("s.unlocking 6")
			s.Unlock()

			stream.Send(in)
			if out, err = stream.CloseAndRecv(); err != nil {
				if err != io.EOF {
					s.errChan <- err
					return
				} else {
					log.Error().Msg("EOF")
					return
				}
			}

			//fmt.Println("s.locking 7")
			s.Lock()
			//defer fmt.Println("s.unlocking 7")
			defer s.Unlock()

			if out.Term > startingTerm {
				fmt.Println("sendHeartbeat: out.Term > startingTerm, becoming follower")
				s.becomeFollower(out.Term)
				return
			}

			if s.state == leader && startingTerm == out.Term {
				if out.Success {
					s.nextIndex[peer.Id] = nextIndex + len(entries)
					s.matchIndex[peer.Id] = nextIndex - 1
					//startingCommitIndex := s.commitIndex
					s.setCommitIndex()
					// if s.commitIndex != startingCommitIndex {
					// 	fmt.Println("foo")
					// 	s.newCommitReady <- struct{}{}
					// 	fmt.Println("bar")
					// }
				} else {
					s.nextIndex[peer.Id] = nextIndex - 1
				}
			}
		}(peer)
	}
}

func (s *RaftServer) setCommitIndex() {
	for i := int(s.commitIndex + 1); i < len(s.log); i++ {
		if s.log[i].Term == s.currentTerm {
			matches := 1
			for _, peer := range s.quorum {
				if s.matchIndex[peer.Id] >= i {
					matches += 1
				}
			}
			if matches*2 > len(s.quorum)+1 {
				s.commitIndex = int32(i)
			}
		}
	}
}
