package main

import (
	"fmt"
	"time"

	"github.com/kleeedolinux/lua-solvm"
)

func main() {
	L := lua.NewState()
	defer L.Close()

	benchmarks := []struct {
		name       string
		code       string
		iterations int
	}{
		{
			name: "String Concatenation (Optimized)",
			code: `
				local t = {}
				for i = 1, 100000 do
					t[i] = "test" .. i
				end
				return #t
			`,
			iterations: 3,
		},
		{
			name: "String Table Operations",
			code: `
				local t = {}
				for i = 1, 100000 do
					t["key" .. i] = "value" .. i
				end
				local sum = 0
				for k, v in pairs(t) do
					sum = sum + #k + #v
				end
				return sum
			`,
			iterations: 3,
		},
		{
			name: "String Pattern Matching",
			code: `
				local s = string.rep("abcdefghijklmnopqrstuvwxyz", 100)
				local count = 0
				for i = 1, 1000 do
					for match in string.gmatch(s, "[aeiou]") do
						count = count + 1
					end
				end
				return count
			`,
			iterations: 3,
		},
		{
			name: "String Format",
			code: `
				local t = {}
				for i = 1, 100000 do
					t[i] = string.format("test%d", i)
				end
				return #t
			`,
			iterations: 3,
		},
		{
			name: "String Substring",
			code: `
				local s = string.rep("abcdefghijklmnopqrstuvwxyz", 1000)
				local sum = 0
				for i = 1, 10000 do
					sum = sum + #string.sub(s, i % #s, (i + 10) % #s)
				end
				return sum
			`,
			iterations: 3,
		},
		{
			name: "String Case Operations",
			code: `
				local s = string.rep("aBcDeFgHiJkLmNoPqRsTuVwXyZ", 100)
				local sum = 0
				for i = 1, 1000 do
					sum = sum + #string.upper(s) + #string.lower(s)
				end
				return sum
			`,
			iterations: 3,
		},
	}

	for _, bm := range benchmarks {
		fmt.Printf("\nRunning benchmark: %s\n", bm.name)
		var totalTime time.Duration
		var bestTime time.Duration
		var worstTime time.Duration

		for i := 0; i < bm.iterations; i++ {
			start := time.Now()
			if err := L.DoString(bm.code); err != nil {
				fmt.Printf("Error in benchmark: %v\n", err)
				continue
			}
			duration := time.Since(start)
			totalTime += duration

			if i == 0 || duration < bestTime {
				bestTime = duration
			}
			if i == 0 || duration > worstTime {
				worstTime = duration
			}

			fmt.Printf("  Iteration %d: %v\n", i+1, duration)
		}

		avgTime := totalTime / time.Duration(bm.iterations)
		fmt.Printf("Results for %s:\n", bm.name)
		fmt.Printf("  Average time: %v\n", avgTime)
		fmt.Printf("  Best time: %v\n", bestTime)
		fmt.Printf("  Worst time: %v\n", worstTime)
	}
}
