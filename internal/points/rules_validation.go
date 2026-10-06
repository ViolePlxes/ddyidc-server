package points

import (
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
)

func ratioReward(cents int64, ratio float64) int64 {
	if cents <= 0 {
		return 0
	}
	rate, ok := new(big.Rat).SetString(strconv.FormatFloat(ratio, 'f', 4, 64))
	if !ok {
		return 0
	}
	value := new(big.Rat).Mul(rate, new(big.Rat).SetInt64(cents))
	value.Quo(value, big.NewRat(10000, 1))
	result := new(big.Int).Quo(value.Num(), value.Denom())
	if !result.IsInt64() {
		return 0
	}
	return result.Int64()
}

func validateLadder(raw json.RawMessage) error {
	var bands []ladderBand
	if json.Unmarshal(raw, &bands) != nil || len(bands) == 0 || len(bands) > 100 {
		return errors.New("请填写有效的阶梯奖励档位")
	}
	for i, b := range bands {
		if b.Min < 0 || b.Max < 0 || (b.Max > 0 && b.Max < b.Min) || b.Points < 0 || b.Points > 1000000 {
			return errors.New("阶梯金额范围或积分无效")
		}
		if i > 0 && (bands[i-1].Max <= 0 || b.Min <= bands[i-1].Max) {
			return errors.New("阶梯档位须按金额递增且不能重叠")
		}
	}
	return nil
}
