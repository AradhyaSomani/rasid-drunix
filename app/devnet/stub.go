package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/queryresult"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// world is the in-memory ledger: committed public state and private collections.
type world struct {
	state   map[string][]byte
	private map[string]map[string][]byte
}

// txStub is one transaction's view of the world. Writes are buffered and only
// applied by commit(), so a failed transaction changes nothing, as on a real peer.
// Methods it does not implement panic via the nil embedded interface.
type txStub struct {
	shim.ChaincodeStubInterface
	w         *world
	txID      string
	fn        string
	args      []string
	creator   []byte
	transient map[string][]byte
	now       time.Time

	writes    map[string][]byte // nil value = delete
	pvtWrites map[string]map[string][]byte
	eventName string
	eventBody []byte
}

func (s *txStub) GetFunctionAndParameters() (string, []string) { return s.fn, s.args }
func (s *txStub) GetStringArgs() []string                      { return append([]string{s.fn}, s.args...) }
func (s *txStub) GetArgs() [][]byte {
	out := [][]byte{[]byte(s.fn)}
	for _, a := range s.args {
		out = append(out, []byte(a))
	}
	return out
}
func (s *txStub) GetTxID() string                          { return s.txID }
func (s *txStub) GetChannelID() string                     { return "rasid" }
func (s *txStub) GetCreator() ([]byte, error)              { return s.creator, nil }
func (s *txStub) GetTransient() (map[string][]byte, error) { return s.transient, nil }
func (s *txStub) GetTxTimestamp() (*timestamppb.Timestamp, error) {
	return timestamppb.New(s.now), nil
}

// Reads see committed state only: as on Fabric, a transaction's own writes
// are invisible to it until it commits.
func (s *txStub) GetState(k string) ([]byte, error) { return s.w.state[k], nil }
func (s *txStub) PutState(k string, v []byte) error { s.writes[k] = v; return nil }
func (s *txStub) DelState(k string) error           { s.writes[k] = nil; return nil }

func (s *txStub) SetEvent(name string, payload []byte) error {
	s.eventName, s.eventBody = name, payload
	return nil
}

func (s *txStub) CreateCompositeKey(t string, a []string) (string, error) {
	return shim.CreateCompositeKey(t, a)
}

func (s *txStub) SplitCompositeKey(k string) (string, []string, error) {
	parts := strings.Split(strings.TrimPrefix(k, "\x00"), "\x00")
	if len(parts) < 2 {
		return "", nil, fmt.Errorf("not a composite key")
	}
	return parts[0], parts[1 : len(parts)-1], nil
}

func (s *txStub) PutPrivateData(c, k string, v []byte) error {
	if s.pvtWrites[c] == nil {
		s.pvtWrites[c] = map[string][]byte{}
	}
	s.pvtWrites[c][k] = v
	return nil
}

func (s *txStub) GetPrivateData(c, k string) ([]byte, error) { return s.w.private[c][k], nil }

// GetStateByPartialCompositeKey sees committed state only, in key order like LevelDB.
func (s *txStub) GetStateByPartialCompositeKey(t string, a []string) (shim.StateQueryIteratorInterface, error) {
	prefix, err := shim.CreateCompositeKey(t, a)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for k := range s.w.state {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	it := &iter{}
	for _, k := range keys {
		it.kvs = append(it.kvs, &queryresult.KV{Key: k, Value: s.w.state[k]})
	}
	return it, nil
}

func (s *txStub) GetStateByPartialCompositeKeyWithPagination(t string, a []string, pageSize int32, bookmark string) (shim.StateQueryIteratorInterface, *peer.QueryResponseMetadata, error) {
	all, err := s.GetStateByPartialCompositeKey(t, a)
	if err != nil {
		return nil, nil, err
	}
	page := &iter{}
	for _, kv := range all.(*iter).kvs {
		if bookmark != "" && kv.Key < bookmark {
			continue
		}
		if int32(len(page.kvs)) == pageSize {
			return page, &peer.QueryResponseMetadata{FetchedRecordsCount: pageSize, Bookmark: kv.Key}, nil
		}
		page.kvs = append(page.kvs, kv)
	}
	return page, &peer.QueryResponseMetadata{FetchedRecordsCount: int32(len(page.kvs))}, nil
}

func (s *txStub) GetHistoryForKey(string) (shim.HistoryQueryIteratorInterface, error) {
	return nil, fmt.Errorf("history is not kept by the devnet ledger")
}

func (s *txStub) commit() {
	for k, v := range s.writes {
		if v == nil {
			delete(s.w.state, k)
		} else {
			s.w.state[k] = v
		}
	}
	for c, kv := range s.pvtWrites {
		if s.w.private[c] == nil {
			s.w.private[c] = map[string][]byte{}
		}
		for k, v := range kv {
			s.w.private[c][k] = v
		}
	}
}

type iter struct {
	kvs []*queryresult.KV
	i   int
}

func (it *iter) HasNext() bool { return it.i < len(it.kvs) }
func (it *iter) Close() error  { return nil }
func (it *iter) Next() (*queryresult.KV, error) {
	kv := it.kvs[it.i]
	it.i++
	return kv, nil
}
