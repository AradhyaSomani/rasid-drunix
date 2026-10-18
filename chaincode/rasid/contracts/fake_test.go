package contracts

import (
	"crypto/x509"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/pkg/cid"
	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/queryresult"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeStub is an in-memory ledger covering the stub calls RASID makes.
// Methods it does not override panic via the nil embedded interface.
// Like Fabric, a transaction's writes are invisible to its own reads; they
// land when the next transaction starts.
type fakeStub struct {
	shim.ChaincodeStubInterface
	state     map[string][]byte
	private   map[string]map[string][]byte
	writes    map[string][]byte // nil value: delete
	pvtWrites map[string]map[string][]byte
	transient map[string][]byte
	events    []string
	lastEvent []byte
	now       time.Time
	txCount   int
}

func newFakeStub(now time.Time) *fakeStub {
	return &fakeStub{state: map[string][]byte{}, private: map[string]map[string][]byte{}, now: now,
		writes: map[string][]byte{}, pvtWrites: map[string]map[string][]byte{}}
}

// commit applies the current transaction's writes.
func (s *fakeStub) commit() {
	for k, v := range s.writes {
		if v == nil {
			delete(s.state, k)
		} else {
			s.state[k] = v
		}
	}
	for c, kvs := range s.pvtWrites {
		if s.private[c] == nil {
			s.private[c] = map[string][]byte{}
		}
		for k, v := range kvs {
			s.private[c][k] = v
		}
	}
	s.writes, s.pvtWrites = map[string][]byte{}, map[string]map[string][]byte{}
}

func (s *fakeStub) GetState(k string) ([]byte, error) { return s.state[k], nil }
func (s *fakeStub) PutState(k string, v []byte) error { s.writes[k] = v; return nil }
func (s *fakeStub) DelState(k string) error           { s.writes[k] = nil; return nil }
func (s *fakeStub) GetTxID() string                   { return fmt.Sprintf("tx%04d", s.txCount) }
func (s *fakeStub) GetTxTimestamp() (*timestamppb.Timestamp, error) {
	return timestamppb.New(s.now), nil
}
func (s *fakeStub) GetTransient() (map[string][]byte, error) { return s.transient, nil }
func (s *fakeStub) SetEvent(name string, payload []byte) error {
	s.events = append(s.events, name)
	s.lastEvent = payload
	return nil
}
func (s *fakeStub) CreateCompositeKey(t string, a []string) (string, error) {
	return shim.CreateCompositeKey(t, a)
}
func (s *fakeStub) SplitCompositeKey(k string) (string, []string, error) {
	parts := strings.Split(strings.TrimPrefix(k, "\x00"), "\x00")
	return parts[0], parts[1 : len(parts)-1], nil
}
func (s *fakeStub) PutPrivateData(c, k string, v []byte) error {
	if s.pvtWrites[c] == nil {
		s.pvtWrites[c] = map[string][]byte{}
	}
	s.pvtWrites[c][k] = v
	return nil
}
func (s *fakeStub) GetPrivateData(c, k string) ([]byte, error) { return s.private[c][k], nil }
func (s *fakeStub) GetStateByPartialCompositeKey(t string, a []string) (shim.StateQueryIteratorInterface, error) {
	prefix, _ := shim.CreateCompositeKey(t, a)
	var keys []string
	for k := range s.state {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	it := &fakeIter{}
	for _, k := range keys {
		it.kvs = append(it.kvs, &queryresult.KV{Key: k, Value: s.state[k]})
	}
	return it, nil
}

func (s *fakeStub) GetStateByPartialCompositeKeyWithPagination(t string, a []string, pageSize int32, bookmark string) (shim.StateQueryIteratorInterface, *peer.QueryResponseMetadata, error) {
	all, _ := s.GetStateByPartialCompositeKey(t, a)
	it := all.(*fakeIter)
	page := &fakeIter{}
	for _, kv := range it.kvs {
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

type fakeIter struct {
	kvs []*queryresult.KV
	i   int
}

func (it *fakeIter) HasNext() bool { return it.i < len(it.kvs) }
func (it *fakeIter) Close() error  { return nil }
func (it *fakeIter) Next() (*queryresult.KV, error) {
	kv := it.kvs[it.i]
	it.i++
	return kv, nil
}

// fakeIdentity stands in for a Fabric CA enrollment certificate.
type fakeIdentity struct {
	msp   string
	attrs map[string]string
}

func (f fakeIdentity) GetID() (string, error)    { return "x509::" + f.msp, nil }
func (f fakeIdentity) GetMSPID() (string, error) { return f.msp, nil }
func (f fakeIdentity) GetAttributeValue(n string) (string, bool, error) {
	v, ok := f.attrs[n]
	return v, ok, nil
}
func (f fakeIdentity) AssertAttributeValue(n, v string) error {
	if f.attrs[n] != v {
		return fmt.Errorf("attribute %s != %s", n, v)
	}
	return nil
}
func (f fakeIdentity) GetX509Certificate() (*x509.Certificate, error) { return nil, nil }

type fakeCtx struct {
	stub *fakeStub
	id   cid.ClientIdentity
}

func (c *fakeCtx) GetStub() shim.ChaincodeStubInterface  { return c.stub }
func (c *fakeCtx) GetClientIdentity() cid.ClientIdentity { return c.id }

// world is a test network: one ledger and a context per actor.
type world struct{ stub *fakeStub }

func newWorld(now time.Time) *world { return &world{stub: newFakeStub(now)} }

// as starts a new transaction signed by the given identity.
func (w *world) as(id fakeIdentity) *fakeCtx {
	w.stub.commit()
	w.stub.txCount++
	w.stub.transient = nil
	return &fakeCtx{stub: w.stub, id: id}
}

func (w *world) withTransient(ctx *fakeCtx, key string, v []byte) *fakeCtx {
	w.stub.transient = map[string][]byte{key: v}
	return ctx
}

func supplier(msp, gstin string) fakeIdentity {
	return fakeIdentity{msp: msp, attrs: map[string]string{attrRole: "supplier", attrGSTIN: gstin}}
}
func buyer(msp, gstin string) fakeIdentity {
	return fakeIdentity{msp: msp, attrs: map[string]string{attrRole: "buyer", attrGSTIN: gstin}}
}
func role(msp string, r Role) fakeIdentity {
	return fakeIdentity{msp: msp, attrs: map[string]string{attrRole: string(r)}}
}
