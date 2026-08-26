# Kafka-Style Message Broker Simulation — Design Document

## Overview

An in-memory simulation of a Kafka-style publish/subscribe message broker: topics
partitioned for parallelism, producers that route messages to partitions by key, and
consumer groups that track per-partition read offsets. No real network I/O —
everything runs in-process using goroutines, channels, and mutex-guarded state. This
is a library, not a server: no HTTP, no CLI, no persistence across process restarts.

**Domain parameters:**
- Package name and module name: `kafkasim`.
- Partition routing is FNV-1a 32-bit hash (`hash/fnv`) of `Message.Key`, mod the
  topic's partition count — deterministic, not round-robin or random.
- Offsets are per-partition, strictly increasing, starting at 0.
- `ConsumerGroup.Subscribe` drains only what's currently in the log at call time — it
  never blocks waiting for future messages.

Out of scope: real network transport, message persistence to disk, partition
rebalancing, replication, consumer group coordination across multiple processes.

## Architecture

```
kafkasim/
├── go.mod
├── types.go     — Offset, Message
├── partition.go — Partition; Append
├── topic.go     — Topic; Produce
├── broker.go    — Broker; NewBroker, CreateTopic, Topic
├── consumer.go  — MessageHandler, ConsumerGroup; NewConsumerGroup, Subscribe,
│                  CommitOffset, CommittedOffset
└── *_test.go    — one test file per source file above, plus a concurrency test
```

All `.go` files use `package kafkasim` at the project root — no subdirectories.

**File assignment rules (strict):**
- `types.go` contains exactly: `Offset`, `Message`. Nothing else.
- `partition.go` contains: `Partition` and `Append` only.
- `topic.go` contains: `Topic` and `Produce` only. `Produce` calls `Partition.Append`
  — it does not duplicate partition-log logic.
- `broker.go` contains: `Broker`, `NewBroker`, `CreateTopic`, `Topic` (the lookup
  method). Do NOT put topic-internal logic (routing, append) here.
- `consumer.go` contains: `MessageHandler`, `ConsumerGroup`, `NewConsumerGroup`,
  `Subscribe`, `CommitOffset`, `CommittedOffset`.

## Data Types and Function Signatures

```go
type Offset int

type Message struct {
    Key   string
    Value []byte
}

type Partition struct {
    mu  sync.Mutex
    log []Message
}

// Append adds msg to the partition's log and returns its assigned offset.
func (p *Partition) Append(msg Message) Offset

type Topic struct {
    Name       string
    Partitions []*Partition
}

// Produce routes msg to one of t's partitions by hashing msg.Key with FNV-1a
// (hash/fnv), mod len(t.Partitions), appends it, and returns the partition
// index and the message's offset within that partition.
func (t *Topic) Produce(msg Message) (partitionIndex int, offset Offset, err error)

type Broker struct {
    mu     sync.Mutex
    topics map[string]*Topic
}

func NewBroker() *Broker

// CreateTopic registers a new topic with numPartitions partitions. Returns an
// error if a topic with this name already exists or numPartitions <= 0.
func (b *Broker) CreateTopic(name string, numPartitions int) (*Topic, error)

// Topic returns the named topic, or an error if it doesn't exist.
func (b *Broker) Topic(name string) (*Topic, error)

// MessageHandler processes one delivered message. Returning a non-nil error
// stops that partition's delivery loop for the current Subscribe call.
type MessageHandler interface {
    Handle(msg Message) error
}

type ConsumerGroup struct {
    mu      sync.Mutex
    name    string
    offsets map[string]map[int]Offset // topic name -> partition index -> next offset to read
}

func NewConsumerGroup(name string) *ConsumerGroup

// Subscribe reads all messages in topic currently at or after this group's
// committed offset for each partition, calling handler.Handle for each one in
// offset order per partition, then returns. It does not block waiting for
// future messages — it drains what's currently in the log and returns.
// Returns ctx.Err() if ctx is cancelled mid-delivery.
func (cg *ConsumerGroup) Subscribe(ctx context.Context, topic *Topic, handler MessageHandler) error

// CommitOffset records that partition `partition` of `topicName` has been
// consumed through `offset` (exclusive) for this group.
func (cg *ConsumerGroup) CommitOffset(topicName string, partition int, offset Offset)

// CommittedOffset returns the next offset to read for this group on the given
// topic/partition (0 if nothing has been committed yet).
func (cg *ConsumerGroup) CommittedOffset(topicName string, partition int) Offset
```

### Export signatures

```go
var _ func(Message) Offset = (*Partition).Append
var _ func(Message) (int, Offset, error) = (*Topic).Produce
var _ func() *Broker = NewBroker
var _ func(*Broker, string, int) (*Topic, error) = (*Broker).CreateTopic
var _ func(*Broker, string) (*Topic, error) = (*Broker).Topic
var _ func(string) *ConsumerGroup = NewConsumerGroup
var _ func(*ConsumerGroup, context.Context, *Topic, MessageHandler) error = (*ConsumerGroup).Subscribe
var _ func(*ConsumerGroup, string, int, Offset) = (*ConsumerGroup).CommitOffset
var _ func(*ConsumerGroup, string, int) Offset = (*ConsumerGroup).CommittedOffset
```

## Behavioral Specification

**`Partition.Append`** is the foundational mutator — it's the only place a message
ever gets added to a log, and it's the only place offsets are assigned. Safe for
concurrent use from multiple goroutines; offsets are assigned in strictly increasing
order per partition starting at 0, meaning the assignment itself must happen while
holding the partition's own mutex (checking `len(p.log)` and appending must be one
atomic critical section, not two separate locked operations — a race between them
would let two goroutines both read the same "next offset" before either appends).

**`Topic.Produce`** composes `Partition.Append`: it selects the partition via
FNV-1a 32-bit hash of `msg.Key` mod `len(t.Partitions)`, then delegates the actual
append (and offset assignment) to that partition. It does not implement its own
offset counter — the returned `offset` is exactly what `Partition.Append` returned.

**`Broker.CreateTopic`/`Broker.Topic`** are pure registry operations over a
name-keyed map; safe for concurrent use. `CreateTopic` with a name that already
exists returns an error without modifying the existing topic — the existing
`*Topic` pointer already handed out to callers must remain valid and unchanged.

**`ConsumerGroup.Subscribe`** depends on `Topic`/`Partition` (reads their logs) and
on the group's own `offsets` map (to know where to start). It snapshots each
partition's current log length once at the start of the call, then delivers exactly
the slice `[committedOffset, snapshotLength)` per partition, in offset order. It
does not implicitly commit — `CommitOffset` is a separate, caller-driven step. If
`handler.Handle` errors, delivery stops for that partition only; other partitions in
the same `Subscribe` call still proceed.

**Function dependency chain**: `Partition.Append` is foundational; `Topic.Produce`
builds on it; `Broker` is an independent registry layer; `ConsumerGroup` depends on
`Topic`/`Partition` for reads and maintains its own offset state independently. Each
of these four is independently testable and should be treated as a separate unit of
work.

## Domain-Specific Test Scenarios

**Why this section applies:** `Topic.Produce`'s partition routing is a specific,
deterministic hash computation (FNV-1a 32-bit, mod partition count) — not round-robin,
not random. A model asked to write a test asserting "key X lands in partition N"
cannot get N right by intuition; unlike a coin-flip or round-robin scheme, the only
way to know the correct partition is to actually compute the hash. Leaving this
unstated means a test-writer either skips the exact-partition assertion (weakening
the test) or guesses a plausible-looking wrong partition. This is the same class of
problem the geometry sections solve for board games, generalized to hash arithmetic.

**Required test scenarios — routing (topic bead):**

**TestProduce/DeterministicRouting:** Topic with 4 partitions. Producing a message
with `Key: "order-1"` routes to **partition 1**: FNV-1a 32-bit hash of `"order-1"` is
`0x2b7fcd6d` (`729795949` as an unsigned 32-bit integer), and
`729795949 mod 4 = 1`. Do NOT assert partition 0, 2, or 3 for this key — those are
plausible-looking guesses, not the computed result.

**TestProduce/DifferentKeyDifferentPartition:** The same topic (4 partitions).
Producing a message with `Key: "order-2"` routes to **partition 0**: FNV-1a 32-bit
hash of `"order-2"` is `679463092`, and `679463092 mod 4 = 0` — a different partition
than `"order-1"` above, demonstrating the routing is key-dependent, not constant.

**TestProduce/SameKeySamePartition:** Producing three messages with `Key: "order-1"`
to the same 4-partition topic all route to **partition 1** (per
`DeterministicRouting` above, since the hash depends only on the key), landing at
offsets 0, 1, and 2 respectively — offsets increase per-message within that one
partition regardless of how many other partitions exist.

Do NOT compute a different hash algorithm (e.g. a simple sum of byte values, or
Go's built-in `map` iteration/hash) — the spec requires `hash/fnv`'s FNV-1a 32-bit
variant specifically; a different hash function will produce different partition
assignments than the ones stated above, and a test asserting the values above will
fail against a differently-implemented (but internally self-consistent) hash choice.

## Cross-Bead Contracts

### Produce → Append (protocol)

- **type**: protocol
- **producer**: topic (topic.go)
- **consumer**: partition (partition.go)
- **interface**: `(*Partition).Append(msg Message) Offset`
- **notes**: `Topic.Produce` must not assign offsets itself — it computes the
  partition index, then delegates entirely to that partition's `Append` for the
  offset. The returned `offset` from `Produce` is exactly `Append`'s return value.

### Subscribe → partition reads (protocol)

- **type**: protocol
- **producer**: partition (partition.go) + topic (topic.go)
- **consumer**: consumer-group (consumer.go)
- **interface**: `Topic.Partitions []*Partition`, `Partition.log` (read via the
  partition's own lock, not exported)
- **notes**: `Subscribe` must snapshot each partition's log length once per call
  before iterating — reading `len(p.log)` fresh per-message would let a message
  produced mid-`Subscribe` call be delivered out of the "currently in the log at
  call time" semantics the behavioral spec requires.

## Decomposition Notes

**Dependency chain**: `types` (Offset, Message) is foundational to everything;
`partition` (Append) is foundational to `topic` (Produce); `broker` is an
independent registry layer over `topic`; `consumer` depends on `topic`/`partition`
for reads. `broker` and `consumer` can be implemented and tested independently of
each other once `topic`/`partition` exist.

**Required concurrency test (partition bead):** `Partition.Append` must be exercised
by concurrent goroutines (e.g. 50 goroutines × 20 messages each, 1000 total) with a
post-hoc check that the resulting log has exactly 1000 entries and every offset
0–999 appears exactly once — not just "no crash," but an explicit duplicate/gap
check on the recorded offsets.

**Integration bead scope** (bounded): create a broker, create a topic with 4
partitions, produce messages with keys `"order-1"` through `"order-8"` (per the
Domain-Specific Test Scenarios routing values above), create a consumer group,
`Subscribe` once and record every delivered message's partition+offset, call
`CommitOffset` for each partition to the highest offset seen, `Subscribe` again and
assert zero messages are redelivered. Do not test rebalancing or multi-group
isolation in the integration bead — those are out of scope entirely.
