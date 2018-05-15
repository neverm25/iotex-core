package trie

import (
	"bytes"
	"container/list"
	"encoding/gob"

	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"

	"github.com/iotexproject/iotex-core/common"
	"github.com/iotexproject/iotex-core/logger"
)

const RADIX = 256

var (
	// ErrInvalidPatricia: invalid operation
	ErrInvalidPatricia = errors.New("invalid patricia operation")

	// ErrPathDiverge: the path diverges
	ErrPathDiverge = errors.New("path diverges")
)

type (
	patricia interface {
		descend([]byte) ([]byte, int, error)
		ascend([]byte, byte) error
		insert([]byte, []byte, *list.List) error
		increase([]byte) (int, int, int)
		collapse([]byte, []byte, byte, bool) ([]byte, []byte, bool)
		blob() ([]byte, error)
		hash() common.Hash32B // hash of this node
		serialize() ([]byte, error)
		deserialize([]byte) error
	}
	// key of next patricia node
	ptrcKey []byte
	// branch is the full node having 256 hashes for next level patricia node + hash of leaf node
	branch struct {
		Path  [RADIX]ptrcKey
		Value []byte
	}
	// leaf is squashed path + actual value (or hash of next patricia node for extension)
	leaf struct {
		Ext   byte // this is an extension node
		Path  ptrcKey
		Value []byte
	}
)

//======================================
// functions for branch
//======================================
// descend returns the key to retrieve next patricia, and length of matching path in bytes
func (b *branch) descend(key []byte) ([]byte, int, error) {
	node := b.Path[key[0]]
	if len(node) > 0 {
		return node, 1, nil
	}
	return nil, 0, errors.Wrapf(ErrInvalidPatricia, "branch does not have path = %d", key[0])
}

// ascend updates the key and returns whether the current node hash to be updated or not
func (b *branch) ascend(key []byte, index byte) error {
	b.Path[index] = nil
	b.Path[index] = make([]byte, common.HashSize)
	copy(b.Path[index], key)
	return nil
}

// insert <k, v> at current patricia node
func (b *branch) insert(k, v []byte, stack *list.List) error {
	node := b.Path[k[0]]
	if len(node) > 0 {
		return errors.Wrapf(ErrInvalidPatricia, "branch already covers path = %d", k[0])
	}
	// create a new leaf
	l := leaf{0, k[1:], v}
	stack.PushBack(&l)
	return nil
}

// increase returns the number of nodes (B, E, L) being added as a result of insert()
func (b *branch) increase(key []byte) (int, int, int) {
	return 0, 0, 1
}

// collapse updates the node, returns the <key, value> if the node can be collapsed
// value is the hash of only remaining leaf node, another DB access is needed to get the actual value
func (b *branch) collapse(k, v []byte, index byte, childClps bool) ([]byte, []byte, bool) {
	// if child cannot collapse, no need to check and return false
	if !childClps {
		return k, v, false
	}
	// value == nil means no entry exist on the incoming path, trim it
	if v == nil {
		b.trim(index)
		k = nil
	}
	// count number of remaining path
	nb := 0
	var key, value []byte
	for i := 0; i < RADIX; i++ {
		if len(b.Path[i]) > 0 {
			nb++
			key = nil
			key = []byte{byte(i)}
			value = b.Path[i]
		}
	}
	// branch can be collapsed if only 1 path remaining
	if nb == 1 {
		logger.Info().Hex("bkey", key).Hex("bvalue", value).Int("remain", nb).Msg("clps branch")
		return append(key, k...), value, true
	}
	return k, v, false
}

// blob return the value stored in the node
func (b *branch) blob() ([]byte, error) {
	// extension node stores the hash to next patricia node
	return nil, errors.Wrap(ErrInvalidPatricia, "branch does not store value")
}

// hash return the hash of this node
func (b *branch) hash() common.Hash32B {
	stream := []byte{}
	for i := 0; i < RADIX; i++ {
		stream = append(stream, b.Path[i]...)
	}
	stream = append(stream, b.Value...)
	return blake2b.Sum256(stream)
}

// serialize to bytes
func (b *branch) serialize() ([]byte, error) {
	var stream bytes.Buffer
	enc := gob.NewEncoder(&stream)
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	// first byte denotes the type of patricia: 2-branch, 1-extension, 0-leaf
	return append([]byte{2}, stream.Bytes()...), nil
}

// deserialize to branch
func (b *branch) deserialize(stream []byte) error {
	// reset variable
	*b = branch{}
	dec := gob.NewDecoder(bytes.NewBuffer(stream[1:]))
	if err := dec.Decode(b); err != nil {
		return err
	}
	return nil
}

func (b *branch) print() {
	for i := 0; i < RADIX; i++ {
		if len(b.Path[i]) > 0 {
			logger.Info().Int("bk", i).Hex("bv", b.Path[i]).Msg("print")
		}
	}
}

func (b *branch) trim(index byte) {
	b.Path[index] = nil
}

//======================================
// functions for leaf
//======================================
// descend returns the key to retrieve next patricia, and length of matching path in bytes
func (l *leaf) descend(key []byte) ([]byte, int, error) {
	match := 0
	for l.Path[match] == key[match] {
		match++
		if match == len(l.Path) {
			return l.Value, match, nil
		}
	}
	return nil, match, ErrPathDiverge
}

// ascend updates the key and returns whether the current node hash to be updated or not
func (l *leaf) ascend(key []byte, index byte) error {
	// leaf node will be replaced by newly created node, no need to update hash
	if l.Ext == 0 {
		return errors.Wrap(ErrInvalidPatricia, "leaf should not exist on path ascending to root")
	}
	l.Value = nil
	l.Value = make([]byte, common.HashSize)
	copy(l.Value, key)
	return nil
}

// insert <k, v> at current patricia node
func (l *leaf) insert(k, v []byte, stack *list.List) error {
	// get the matching length
	match := 0
	for l.Path[match] == k[match] {
		match++
	}
	// insert() gets called b/c path does not totally match so the below should not happen, but check anyway
	if match == len(l.Path) {
		return errors.Wrapf(ErrInvalidPatricia, "leaf already has total matching path = %x", l.Path)
	}
	if l.Ext == 1 {
		// by definition extension should not have path length = 1 -- that should've been created as branch
		if len(l.Path) == 1 {
			return errors.Wrap(ErrInvalidPatricia, "ext should not have path length = 1")
		}
		// split the current extension
		logger.Debug().Hex("new key", k[match:]).Msg("diverge")
		if err := l.split(match, k[match:], v, stack); err != nil {
			return err
		}
		n := stack.Front()
		ptr, _ := n.Value.(patricia)
		hash := ptr.hash()
		//======================================
		// the matching part becomes a new node leading to top of split
		// match == 1
		// current E -> B[P[0]] -> top of split
		// match > 1
		// current E -> E <P[:match]> -> top of split
		//======================================
		if match == 1 {
			b := branch{}
			b.Path[l.Path[0]] = hash[:]
			hashb := b.hash()
			logger.Debug().Hex("topB", hashb[:8]).Hex("path", l.Path[0:1]).Msg("split")
			stack.PushFront(&b)
		} else if match > 1 {
			e := leaf{Ext: 1}
			e.Path = l.Path[:match]
			e.Value = hash[:]
			hashe := e.hash()
			logger.Debug().Hex("topE", hashe[:8]).Hex("path", l.Path[:match]).Msg("split")
			stack.PushFront(&e)
		}
		return nil
	}
	// add 2 leaf, l1 is current node, l2 for new <key, value>
	l1 := leaf{0, l.Path[match+1:], l.Value}
	hashl1 := l1.hash()
	l2 := leaf{0, k[match+1:], v}
	hashl2 := l2.hash()
	// add 1 branch to link 2 new leaf
	b := branch{}
	b.Path[l.Path[match]] = hashl1[:]
	b.Path[k[match]] = hashl2[:]
	stack.PushBack(&b)
	stack.PushBack(&l1)
	stack.PushBack(&l2)
	// if there's matching part, add 1 ext leading to new branch
	if match > 0 {
		hashb := b.hash()
		e := leaf{1, k[:match], hashb[:]}
		stack.PushFront(&e)
	}
	return nil
}

// increase returns the number of nodes (B, E, L) being added as a result of insert()
func (l *leaf) increase(key []byte) (int, int, int) {
	// get the matching length
	match := 0
	for l.Path[match] == key[match] {
		match++
	}
	if match > 0 {
		return 1, 1, 2
	}
	return 1, 0, 2
}

// collapse updates the node, returns the <key, value> if the node can be collapsed
func (l *leaf) collapse(k, v []byte, index byte, childCollapse bool) ([]byte, []byte, bool) {
	// if child cannot collapse, no need to check and return false
	if !childCollapse {
		return k, v, false
	}
	return append(l.Path, k...), v, true
}

// blob return the value stored in the node
func (l *leaf) blob() ([]byte, error) {
	if l.Ext == 1 {
		// extension node stores the hash to next patricia node
		return nil, errors.Wrap(ErrInvalidPatricia, "extension does not store value")
	}
	return l.Value, nil
}

// hash return the hash of this node
func (l *leaf) hash() common.Hash32B {
	stream := append([]byte{l.Ext}, l.Path...)
	stream = append(stream, l.Value...)
	return blake2b.Sum256(stream)
}

// serialize to bytes
func (l *leaf) serialize() ([]byte, error) {
	stream := bytes.Buffer{}
	enc := gob.NewEncoder(&stream)
	if err := enc.Encode(l); err != nil {
		return nil, err
	}
	// first byte denotes the type of patricia: 2-branch, 1-extension, 0-leaf
	return append([]byte{l.Ext}, stream.Bytes()...), nil
}

// deserialize to leaf
func (l *leaf) deserialize(stream []byte) error {
	// reset variable
	*l = leaf{}
	dec := gob.NewDecoder(bytes.NewBuffer(stream[1:]))
	if err := dec.Decode(l); err != nil {
		return err
	}
	return nil
}

// split diverging path
//======================================
// len(k) == 1
// E -> B[P[0]] -> E.value>
//      B[k[0]] -> Leaf <k[1:], v> this is the <k, v> to be inserted
// len(k) == 2
// E -> B[P[0]] -> B1[P[1]] -> E.value>
//      B[k[0]] -> Leaf <k[1:], v> this is the <k, v> to be inserted
// len(k) > 2
// E -> B[P[0]] -> E <P[1:]], E.value>
//      B[k[0]] -> Leaf <k[1:], v> this is the <k, v> to be inserted
//======================================
func (l *leaf) split(match int, k, v []byte, stack *list.List) error {
	var node patricia = nil
	divPath := l.Path[match:]
	logger.Debug().Hex("curr key", divPath).Msg("diverge")
	// add leaf for new <k, v>
	l1 := leaf{0, k[1:], v}
	hashl := l1.hash()
	logger.Debug().Hex("L", hashl[:8]).Hex("path", k[1:]).Msg("split")
	// add 1 branch to link new leaf and current ext (which may split as below)
	b := branch{}
	b.Path[k[0]] = hashl[:]
	switch len(divPath) {
	case 1:
		b.Path[divPath[0]] = l.Value
		logger.Warn().Hex("L", hashl[:8]).Hex("path", divPath[0:1]).Msg("split")
	case 2:
		// add another branch to split current ext
		b1 := branch{}
		b1.Path[divPath[1]] = l.Value
		hashb := b1.hash()
		logger.Debug().Hex("B1", hashb[:8]).Hex("k", divPath[1:2]).Hex("v", l.Value).Msg("split")
		node = &b1
		// link new leaf and current ext (which becomes b1)
		b.Path[divPath[0]] = hashb[:]
	default:
		// add 1 ext to split current ext
		e := leaf{1, divPath[1:], l.Value}
		hashe := e.hash()
		logger.Debug().Hex("E", hashe[:8]).Hex("k", divPath[1:]).Hex("v", l.Value).Msg("split")
		node = &e
		// link new leaf and current ext (which becomes e)
		b.Path[divPath[0]] = hashe[:]
	}
	hashb := b.hash()
	stack.PushBack(&b)
	logger.Debug().Hex("B", hashb[:8]).Hex("path", k[0:1]).Msg("split")
	if node != nil {
		stack.PushBack(node)
	}
	stack.PushBack(&l1)
	return nil
}
