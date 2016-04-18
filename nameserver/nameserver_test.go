package nameserver

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/weaveworks/mesh"

	"github.com/weaveworks/weave/net/address"
	"github.com/weaveworks/weave/testing/gossip"
)

func makeNameserver(name mesh.PeerName) *Nameserver {
	return New(name, "", NewMockDB(), func(mesh.PeerName) bool { return true })
}

func makeNetwork(size int) ([]*Nameserver, *gossip.TestRouter) {
	gossipRouter := gossip.NewTestRouter(0.0)
	nameservers := make([]*Nameserver, size)

	for i := 0; i < size; i++ {
		name, _ := mesh.PeerNameFromString(fmt.Sprintf("%02d:00:00:02:00:00", i))
		nameserver := makeNameserver(name)
		nameserver.SetGossip(gossipRouter.Connect(nameserver.ourName, nameserver))
		nameserver.Start()
		nameservers[i] = nameserver
	}

	return nameservers, gossipRouter
}

func stopNetwork(nameservers []*Nameserver, grouter *gossip.TestRouter) {
	for _, nameserver := range nameservers {
		nameserver.Stop()
	}
	grouter.Stop()
}

type pair struct {
	origin mesh.PeerName
	addr   address.Address
}

type mapping struct {
	hostname string
	addrs    []pair
}

type addrs []address.Address

func (a addrs) Len() int           { return len(a) }
func (a addrs) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a addrs) Less(i, j int) bool { return a[i] < a[j] }
func (a addrs) String() string {
	ss := []string{}
	for _, addr := range a {
		ss = append(ss, addr.String())
	}
	return strings.Join(ss, " ")
}

func (m mapping) Addrs() []address.Address {
	want := addrs{}
	for _, p := range m.addrs {
		want = append(want, p.addr)
	}
	sort.Sort(want)
	return want
}

func TestNameservers(t *testing.T) {
	//common.SetLogLevel("debug")

	lookupTimeout := 10 // ms
	nameservers, grouter := makeNetwork(30)
	defer stopNetwork(nameservers, grouter)
	// This subset will sometimes lose touch with some of the others
	badNameservers := nameservers[25:]
	// This subset will remain well-connected, and we will deal mainly with them
	nameservers = nameservers[:25]
	nameserversByName := map[mesh.PeerName]*Nameserver{}
	for _, n := range nameservers {
		nameserversByName[n.ourName] = n
	}
	mappings := []mapping{}

	check := func(nameserver *Nameserver, expected mapping) {
		have := []address.Address{}
		for i := 0; i < lookupTimeout; i++ {
			have = nameserver.Lookup(expected.hostname)
			sort.Sort(addrs(have))
			if reflect.DeepEqual(have, expected.Addrs()) {
				return
			}
			time.Sleep(1 * time.Millisecond)
		}
		want := expected.Addrs()
		require.Equal(t, addrs(want).String(), addrs(have).String())
	}

	addMapping := func() {
		nameserver := nameservers[rand.Intn(len(nameservers))]
		addr := address.Address(rand.Int31())
		// Create a hostname which has some upper and lowercase letters,
		// and a unique number so we don't have to check if we allocated it already
		randomBits := rand.Int63()
		firstLetter := 'H' + (randomBits&1)*32
		secondLetter := 'O' + (randomBits&2)*16
		randomBits = randomBits >> 2
		hostname := fmt.Sprintf("%c%cstname%d", firstLetter, secondLetter, randomBits)
		mapping := mapping{hostname, []pair{{nameserver.ourName, addr}}}
		mappings = append(mappings, mapping)

		nameserver.AddEntry(hostname, "", nameserver.ourName, addr, false)
		check(nameserver, mapping)
	}

	addExtraMapping := func() {
		if len(mappings) <= 0 {
			return
		}
		nameserver := nameservers[rand.Intn(len(nameservers))]
		i := rand.Intn(len(mappings))
		mapping := mappings[i]
		addr := address.Address(rand.Int31())
		mapping.addrs = append(mapping.addrs, pair{nameserver.ourName, addr})
		mappings[i] = mapping

		nameserver.AddEntry(mapping.hostname, "", nameserver.ourName, addr, false)
		check(nameserver, mapping)
	}

	loseConnection := func() {
		nameserver1 := badNameservers[rand.Intn(len(badNameservers))]
		nameserver2 := nameservers[rand.Intn(len(nameservers))]
		nameserver1.PeerGone(nameserver2.ourName)
	}

	deleteMapping := func() {
		if len(mappings) <= 0 {
			return
		}
		i := rand.Intn(len(mappings))
		mapping := mappings[i]
		if len(mapping.addrs) <= 0 {
			return
		}
		j := rand.Intn(len(mapping.addrs))
		pair := mapping.addrs[j]
		mapping.addrs = append(mapping.addrs[:j], mapping.addrs[j+1:]...)
		mappings[i] = mapping
		nameserver := nameserversByName[pair.origin]

		nameserver.Delete(mapping.hostname, "*", pair.addr.String(), pair.addr)
		check(nameserver, mapping)
	}

	doLookup := func() {
		if len(mappings) <= 0 {
			return
		}
		mapping := mappings[rand.Intn(len(mappings))]
		nameserver := nameservers[rand.Intn(len(nameservers))]
		check(nameserver, mapping)
	}

	doReverseLookup := func() {
		if len(mappings) <= 0 {
			return
		}
		mapping := mappings[rand.Intn(len(mappings))]
		if len(mapping.addrs) <= 0 {
			return
		}
		nameserver := nameservers[rand.Intn(len(nameservers))]
		hostname := ""
		var err error
		for i := 0; i < lookupTimeout; i++ {
			hostname, err = nameserver.ReverseLookup(mapping.addrs[0].addr)
			if err != nil && mapping.hostname == hostname {
				return
			}
			time.Sleep(1 * time.Millisecond)
		}
		require.Nil(t, err)
		require.Equal(t, mapping.hostname, hostname)
	}

	for i := 0; i < 800; i++ {
		r := rand.Float32()
		switch {
		case r < 0.1:
			addMapping()

		case 0.1 <= r && r < 0.2:
			addExtraMapping()

		case 0.2 <= r && r < 0.3:
			deleteMapping()

		case 0.3 <= r && r < 0.35:
			loseConnection()

		case 0.35 <= r && r < 0.9:
			doLookup()

		case 0.9 <= r:
			doReverseLookup()
		}

		grouter.Flush()
	}
}

func TestContainerAndPeerDeath(t *testing.T) {
	peername, err := mesh.PeerNameFromString("00:00:00:02:00:00")
	require.Nil(t, err)
	nameserver := makeNameserver(peername)

	nameserver.AddEntry("hostname", "containerid", peername, address.Address(0), false)
	require.Equal(t, []address.Address{0}, nameserver.Lookup("hostname"))

	nameserver.ContainerDied("containerid")
	require.Equal(t, []address.Address{}, nameserver.Lookup("hostname"))

	nameserver.AddEntry("hostname", "containerid", peername, address.Address(0), false)
	require.Equal(t, []address.Address{0}, nameserver.Lookup("hostname"))

	nameserver.PeerGone(peername)
	require.Equal(t, []address.Address{}, nameserver.Lookup("hostname"))
}

func TestTombstoneDeletion(t *testing.T) {
	oldNow := now
	defer func() { now = oldNow }()
	now = func() int64 { return 1234 }

	peername, err := mesh.PeerNameFromString("00:00:00:02:00:00")
	require.Nil(t, err)
	nameserver := makeNameserver(peername)

	nameserver.AddEntry("hostname", "containerid", peername, address.Address(0), false)
	require.Equal(t, []address.Address{0}, nameserver.Lookup("hostname"))

	nameserver.deleteTombstones()
	require.Equal(t, []address.Address{0}, nameserver.Lookup("hostname"))

	nameserver.Delete("hostname", "containerid", "", address.Address(0))
	require.Equal(t, []address.Address{}, nameserver.Lookup("hostname"))
	require.Equal(t, l(Entries{Entry{
		ContainerID: "containerid",
		Origin:      peername,
		Addr:        address.Address(0),
		Hostname:    "hostname",
		Version:     1,
		Tombstone:   1234,
	}}), nameserver.entries)

	now = func() int64 { return 1234 + int64(tombstoneTimeout/time.Second) + 1 }
	nameserver.deleteTombstones()
	require.Equal(t, Entries{}, nameserver.entries)
}

// TestRestoration tests the restoration of local entries procedure.
func TestRestoration(t *testing.T) {
	const (
		container1 = "c1"
		container2 = "c2"
		container3 = "c3"
		hostname1  = "hostname1"
		hostname2  = "hostname2"
		addr1      = address.Address(1)
		addr2      = address.Address(2)
		addr3      = address.Address(3)
	)

	oldNow := now
	defer func() { now = oldNow }()
	now = func() int64 { return 1234 }

	name, _ := mesh.PeerNameFromString("00:00:00:02:00:00")
	nameserver := makeNameserver(name)

	nameserver.AddEntry(hostname1, container1, name, addr1, false)
	nameserver.AddEntry(hostname2, container2, name, addr2, false)
	nameserver.AddEntry(hostname2, container3, name, addr3, false)
	nameserver.Delete(hostname2, container2, "", addr2)

	// "Restart" nameserver by creating a new instance with the reused db instance
	now = func() int64 { return 4321 }
	nameserver = New(name, "", nameserver.db, func(mesh.PeerName) bool { return true })

	nameserver.RLock()
	require.Equal(t,
		Entries{
			Entry{
				ContainerID: container1,
				Origin:      name,
				Addr:        addr1,
				Hostname:    hostname1,
				lHostname:   hostname1,
				Version:     1,
				Tombstone:   4321,
				stopped:     true,
			},
			Entry{
				ContainerID: container2,
				Origin:      name,
				Addr:        addr2,
				Hostname:    hostname2,
				lHostname:   hostname2,
				Version:     1,
				Tombstone:   1234,
				stopped:     false,
			},
			Entry{
				ContainerID: container3,
				Origin:      name,
				Addr:        addr3,
				Hostname:    hostname2,
				lHostname:   hostname2,
				Version:     1,
				Tombstone:   4321,
				stopped:     true,
			},
		}, nameserver.entries, "")
	nameserver.RUnlock()
}

// TestAddEntryWithRestore tests whether stopped entries have been restored and
// broadcasted to the peers.
func TestAddEntryWithRestore(t *testing.T) {
	const (
		container1 = "c1"
		container2 = "c2"
		container3 = "c3"
		hostname1  = "hostname1"
		hostname2  = "hostname2"
		hostname3  = "hostname3"
		hostname4  = "hostname4"
		addr1      = address.Address(1)
		addr2      = address.Address(2)
		addr3      = address.Address(3)
		addr4      = address.Address(4)
	)

	oldNow := now
	defer func() { now = oldNow }()
	now = func() int64 { return 1234 }

	nameservers, grouter := makeNetwork(2)
	defer stopNetwork(nameservers, grouter)
	ns1, ns2 := nameservers[0], nameservers[1]

	ns1.AddEntry(hostname1, container1, ns1.ourName, addr1, false)
	ns1.AddEntry(hostname2, container2, ns1.ourName, addr2, false)
	ns1.Delete(hostname2, container2, "", addr2)

	// Restart ns1 and preserve its db instance for marking the first entry as stopped
	time.Sleep(1 * time.Millisecond)
	ns1.Stop()
	grouter.RemovePeer(ns1.ourName)
	// TODO(mp) possible race: 1) PeerGone 2) OnGossipBroadcast
	ns2.PeerGone(ns1.ourName)
	nameservers[0] = New(ns1.ourName, "", ns1.db, func(mesh.PeerName) bool { return true })
	ns1 = nameservers[0]
	ns1.SetGossip(grouter.Connect(ns1.ourName, ns1))
	ns1.Start()
	// At this point, the c1 entry is set to stopped

	// Because restoreStopped is not set, the c1 entry won't be set back to "normal"
	ns1.AddEntry(hostname3, container3, ns1.ourName, addr3, false)
	ns1.RLock()
	require.Equal(t,
		Entry{
			ContainerID: container1,
			Origin:      ns1.ourName,
			Addr:        addr1,
			Hostname:    hostname1,
			lHostname:   hostname1,
			Version:     1,
			Tombstone:   1234,
			stopped:     true,
		}, ns1.entries[0], "")
	ns1.RUnlock()

	time.Sleep(1 * time.Millisecond)
	// ns2 should store only the c3 entry
	ns2.RLock()
	require.Len(t, ns2.entries, 1, "")
	require.Equal(t, container3, ns2.entries[0].ContainerID, "")
	ns2.RUnlock()

	ns1.AddEntry(hostname4, container1, ns1.ourName, addr4, true)
	// c1 (hostname1 -> addr1) should be restored and propagated to ns2
	time.Sleep(1 * time.Millisecond)

	ns2.RLock()
	require.Len(t, ns2.entries, 3, "")
	require.Equal(t,
		Entry{
			ContainerID: container1,
			Origin:      ns1.ourName,
			Addr:        addr1,
			Hostname:    hostname1,
			lHostname:   hostname1,
			Version:     2,
			Tombstone:   0,
			stopped:     false,
		}, ns2.entries[0], "")
	ns2.RUnlock()

	//fmt.Println("NS1>", ns1.entries)
	//// TODO(mp) once we got obsolete entry for c2, so make sure that we broadcast
	//// tombstoned entries during the restoration procedure. Add tests for that
	//fmt.Println("NS2>", ns2.entries)

	grouter.Flush()
}

// Database mock

type MockDB struct {
	data map[string]Entries
}

func NewMockDB() *MockDB {
	return &MockDB{data: make(map[string]Entries)}
}

func (m *MockDB) Load(key string, val interface{}) (bool, error) {
	if entries, ok := m.data[key]; ok {
		ret := make(Entries, len(entries))
		copy(ret, entries)
		valPtr := val.(*Entries)
		*valPtr = ret
		return true, nil
	}
	return false, nil
}

func (m *MockDB) Save(key string, val interface{}) error {
	entries := val.(Entries)
	m.data[key] = make(Entries, len(entries))
	copy(m.data[key], entries)
	return nil
}
