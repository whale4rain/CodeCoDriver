package lease

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisDefaultTTL = 45 * time.Second
)

type RedisLeaser struct {
	client   *redis.Client
	workerID string
}

func NewRedis(ctx context.Context, addr string) (*RedisLeaser, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return &RedisLeaser{client: client, workerID: fmt.Sprintf("worker-%d", time.Now().UnixNano())}, nil
}

func (r *RedisLeaser) Close() error {
	return r.client.Close()
}

func (r *RedisLeaser) TryClaim(ctx context.Context, taskID string, ttl time.Duration) (Lease, bool, error) {
	if ttl <= 0 {
		ttl = redisDefaultTTL
	}
	token, err := r.client.Incr(ctx, r.fencingKey(taskID)).Result()
	if err != nil {
		return Lease{}, false, err
	}
	value := fmt.Sprintf("%s:%d", r.workerID, token)
	ok, err := r.client.SetNX(ctx, r.leaseKey(taskID), value, ttl).Result()
	if err != nil {
		return Lease{}, false, err
	}
	if !ok {
		return Lease{}, false, nil
	}
	return Lease{TaskID: taskID, WorkerID: r.workerID, Token: token}, true, nil
}

func (r *RedisLeaser) Renew(ctx context.Context, l Lease, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = redisDefaultTTL
	}
	value := fmt.Sprintf("%s:%d", l.WorkerID, l.Token)
	script := redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)
	result, err := script.Run(ctx, r.client, []string{r.leaseKey(l.TaskID)}, value, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrStaleLease
	}
	return nil
}

func (r *RedisLeaser) Release(ctx context.Context, l Lease) error {
	value := fmt.Sprintf("%s:%d", l.WorkerID, l.Token)
	script := redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)
	_, err := script.Run(ctx, r.client, []string{r.leaseKey(l.TaskID)}, value).Int()
	return err
}

func (r *RedisLeaser) leaseKey(taskID string) string {
	return "codecodriver:task:" + taskID + ":lease"
}

func (r *RedisLeaser) fencingKey(taskID string) string {
	return "codecodriver:task:" + taskID + ":fencing"
}

func ParseToken(value string) int64 {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == ':' {
			token, _ := strconv.ParseInt(value[i+1:], 10, 64)
			return token
		}
	}
	return 0
}
