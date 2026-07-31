package until

import (
	"errors"
	"sync"
	"time"
)

var epoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano() / 1e6

var IdGenerate *Snowflake

// Snowflake 结构体
type Snowflake struct {
	machineID     uint64     // 机器ID (0-63)
	sequence      uint64     // 序列号 (0-63)
	lastTimestamp uint64     // 上次生成ID的时间戳
	mutex         sync.Mutex // 互斥锁，保证并发安全
	startTime     uint64     // 起始时间（毫秒时间戳）
}

const (
	MachineIDBits = 6                          // 机器ID位数
	SequenceBits  = 6                          // 序列号位数
	MaxMachineID  = -1 ^ (-1 << MachineIDBits) // 最大机器ID (63)
	MaxSequence   = -1 ^ (-1 << SequenceBits)  // 最大序列号 (63)

	Min16BitID = 1000000000000000
	Max16BitID = 9007199254740991
	Range16Bit = Max16BitID - Min16BitID + 1
)

// NewSnowflake 创建一个新的Snowflake实例
// machineID: 0 到 maxMachineID
func NewSnowflake(machineID uint64) (*Snowflake, error) {
	if machineID > MaxMachineID {
		return nil, errors.New("machine ID out of range [0-63]")
	}

	// 将起始时间转为毫秒时间戳
	epoch := uint64(epoch)
	if epoch <= 0 {
		return nil, errors.New("invalid start time")
	}

	return &Snowflake{
		machineID:     machineID,
		sequence:      0,
		lastTimestamp: 0,
		startTime:     epoch,
	}, nil
}

// NextId 生成一个唯一的16位十进制数ID
func (s *Snowflake) NextId() (uint64, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 获取当前时间（毫秒）
	currentTime := time.Now().UnixNano() / 1e6
	if currentTime <= 0 {
		return 0, errors.New("invalid current time")
	}
	currentTimestamp := uint64(currentTime) - s.startTime

	// 检查时间回拨
	if currentTimestamp < s.lastTimestamp {
		return 0, errors.New("clock moved backwards")
	}

	// 如果是同一毫秒内
	if currentTimestamp == s.lastTimestamp {
		s.sequence = (s.sequence + 1) & MaxSequence

		// 当前毫秒序列号用完，等待下一毫秒
		if s.sequence == 0 {
			for currentTimestamp <= s.lastTimestamp {
				currentTime = time.Now().UnixNano() / 1e6
				currentTimestamp = uint64(currentTime) - s.startTime
			}
		}
	} else {
		s.sequence = 0
	}

	s.lastTimestamp = currentTimestamp

	// 42位二进制数 (30位时间戳 + 6位机器ID + 6位序列号)
	bitID := (currentTimestamp << (MachineIDBits + SequenceBits)) |
		(s.machineID << SequenceBits) |
		s.sequence

	// 压缩42位到16位十进制
	return compressTo16Bit(bitID), nil
}

func compressTo16Bit(id uint64) uint64 {
	// 1. 对原始ID进行Fibonacci散列
	fibID := id * 11400714819323198485 // 2^64/φ (黄金分割比例)

	// 2. 映射到16位十进制范围
	scaledID := fibID % Range16Bit

	// 3. 确保在指定范围内
	return Min16BitID + scaledID
}

func init() {
	g, err := NewSnowflake(32)
	if err != nil {
		return
	}
	IdGenerate = g
}
