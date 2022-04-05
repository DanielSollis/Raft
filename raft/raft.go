package raft

import (
	"Raft/api"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Peer struct {
	Id      string `json:"id"`
	Port    int    `json:"port"`
	Address string
	Client  api.RaftClient
}

type RaftServer struct {
	sync.Mutex
	api.UnimplementedRaftServer

	// Self identification
	id            string
	port          int
	address       string
	srv           *grpc.Server
	quorum        []Peer
	lastHeartbeat time.Time

	// Persistent state on all servers
	currentTerm int32        // Latest term server has seen
	votedFor    string       // Candidate Id that received vote in current term (nil if none)
	log         []*api.Entry // Contains command and term of when received by leader (first index is 1)

	// Volatile state on all servers
	commitIndex int32 // Index of the highest log entry known to be replicated
	lastApplied int64 // Index of highest log entry applied to state machine
	state       State // Leader, candidate or follower

	// Volatile state on leaders (Reinitialized after election)
	nextIndex  map[string]int // Index of the next log entry to send to followers (initialized to leader's last log index + 1)
	matchIndex map[string]int // Index of highest log entry known to be replicated on follower (initialized to 0)

	// channels
	shutdown       chan os.Signal
	newCommitReady chan struct{}
	commitChan     chan<- api.Entry
	errChan        chan error
}

func NewRaftServer(port int, id string) (err error) {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	raftNode := RaftServer{srv: grpc.NewServer()}

	// IDs
	raftNode.id = id
	raftNode.port = port
	raftNode.address = fmt.Sprintf("localhost:%d", port)
	if raftNode.quorum, err = raftNode.findQuorum(); err != nil {
		return err
	}
	fmt.Printf("node's quorum: %v\n", raftNode.quorum)

	// Persistent server state
	raftNode.currentTerm = 0
	raftNode.votedFor = ""
	raftNode.log = []*api.Entry{}

	// Volatile server state
	raftNode.commitIndex = 0
	raftNode.lastApplied = 0
	raftNode.state = follower

	// Volatile leader state
	raftNode.nextIndex = make(map[string]int)
	raftNode.matchIndex = make(map[string]int)

	// Channels
	raftNode.errChan = make(chan error, 1)
	raftNode.shutdown = make(chan os.Signal, 2)
	signal.Notify(raftNode.shutdown, os.Interrupt)

	api.RegisterRaftServer(raftNode.srv, &raftNode)

	// Create a socket on the specified port
	var listener net.Listener
	if listener, err = net.Listen("tcp", raftNode.address); err != nil {
		log.Error().Msg(err.Error())
		return err
	}

	// Start timers
	go func() {
		fmt.Println("ServeRaft: running election timer")
		rand.New(rand.NewSource(int64(port)))
		raftNode.lastHeartbeat = time.Now()
		raftNode.runElectionTimer()
	}()

	// Serve raft node
	fmt.Printf("serving %v on %v\n", id, raftNode.address)
	go raftNode.commitChannelSender()
	err = raftNode.Serve(listener)
	return err
}

func (s *RaftServer) findQuorum() (quorum []Peer, err error) {
	jsonBytes, err := os.ReadFile("config.json")
	if err != nil {
		return nil, err
	}

	var peers []Peer
	if err = json.Unmarshal(jsonBytes, &peers); err != nil {
		return nil, err
	}

	for _, peer := range peers {
		if peer.Id != s.id {
			peer.Address = fmt.Sprintf("localhost:%d", peer.Port)
			if peer.Client, err = CreateClient(peer.Address); err != nil {
				return nil, err
			}
			quorum = append(quorum, peer)
			fmt.Printf("dsaf")
		}
	}
	return quorum, nil
}

func CreateClient(address string) (client api.RaftClient, err error) {
	fmt.Println("Creating Raft Peer Client")

	opts := grpc.WithTransportCredentials(insecure.NewCredentials())

	var conn *grpc.ClientConn
	if conn, err = grpc.Dial(address, opts); err != nil {
		return nil, err
	}
	client = api.NewRaftClient(conn)

	return client, nil
}

func (s *RaftServer) Serve(listener net.Listener) error {
	go func() {
		err := <-s.errChan
		fmt.Printf("encountered error: %v\n", err)
		s.gracefulShutdown()
	}()
	err := s.srv.Serve(listener)
	return err
}

func (s *RaftServer) gracefulShutdown() {
	fmt.Printf("Stopping server on %v\n", s.address)
	s.srv.Stop()
	os.Exit(1)
}

func (s *RaftServer) commitChannelSender() {
	for range s.newCommitReady {
		s.Lock()

		var entries []*api.Entry
		startingTerm := s.currentTerm
		startingLastApplied := s.lastApplied
		if s.commitIndex > int32(s.lastApplied) {
			entries = s.log[s.lastApplied+1 : s.commitIndex+1]
			s.lastApplied = int64(s.commitIndex)
		}

		s.Unlock()

		for i, entry := range entries {
			s.commitChan <- api.Entry{
				Value: entry.Value,
				Index: int32(startingLastApplied) + int32(i+1),
				Term:  startingTerm,
			}
		}
	}
}

func (s *RaftServer) Submit(ctx context.Context, req *api.SubmitRequest) (*api.SubmitReply, error) {
	s.Lock()
	defer s.Unlock()

	if s.state == leader {
		entry := &api.Entry{
			Term:  s.currentTerm,
			Index: int32(len(s.log)),
			Value: req.Value,
		}
		s.log = append(s.log, entry)
		return &api.SubmitReply{Success: true}, nil
	}
	return &api.SubmitReply{Success: false}, nil
}
