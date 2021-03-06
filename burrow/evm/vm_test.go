// Copyright 2017 Monax Industries Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"errors"

	. "github.com/JincorTech/hyperledger-fabric-evmcc/burrow/evm/opcodes"
	ptypes "github.com/JincorTech/hyperledger-fabric-evmcc/burrow/permission/types"
	"github.com/JincorTech/hyperledger-fabric-evmcc/burrow/txs"
	. "github.com/JincorTech/hyperledger-fabric-evmcc/burrow/word256"
	"github.com/stretchr/testify/assert"
	"github.com/tendermint/go-events"
)

func init() {
	SetDebug(true)
}

func newAppState() *FakeAppState {
	fas := &FakeAppState{
		accounts: make(map[string]*Account),
		storage:  make(map[string]Word256),
	}
	// For default permissions
	fas.accounts[ptypes.GlobalPermissionsAddress256.String()] = &Account{
		Permissions: ptypes.DefaultAccountPermissions,
	}
	return fas
}

func newParams() Params {
	return Params{
		BlockHeight: 0,
		BlockHash:   Zero256,
		BlockTime:   0,
		GasLimit:    0,
	}
}

func makeBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// Runs a basic loop
func TestVM(t *testing.T) {
	ourVm := NewVM(newAppState(), DefaultDynamicMemoryProvider, newParams(), Zero256, nil)

	// Create accounts
	account1 := &Account{
		Address: Int64ToWord256(100),
	}
	account2 := &Account{
		Address: Int64ToWord256(101),
	}

	var gas int64 = 500000 // @NOTE: Was fixed
	N := []byte{0x0f, 0x0f}
	// Loop N times
	code := []byte{0x60, 0x00, 0x60, 0x20, 0x52, 0x5B, byte(0x60 + len(N) - 1)}
	code = append(code, N...)
	code = append(code, []byte{0x60, 0x20, 0x51, 0x12, 0x15, 0x60, byte(0x1b + len(N)), 0x57, 0x60, 0x01, 0x60, 0x20, 0x51, 0x01, 0x60, 0x20, 0x52, 0x60, 0x05, 0x56, 0x5B}...)
	start := time.Now()
	output, err := ourVm.Call(account1, account2, code, []byte{}, 0, &gas)
	fmt.Printf("Output: %v Error: %v\n", output, err)
	fmt.Println("Call took:", time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
}

func TestJumpErr(t *testing.T) {
	ourVm := NewVM(newAppState(), DefaultDynamicMemoryProvider, newParams(), Zero256, nil)

	// Create accounts
	account1 := &Account{
		Address: Int64ToWord256(100),
	}
	account2 := &Account{
		Address: Int64ToWord256(101),
	}

	var gas int64 = 100000
	code := []byte{0x60, 0x10, 0x56} // jump to position 16, a clear failure
	var err error
	ch := make(chan struct{})
	go func() {
		_, err = ourVm.Call(account1, account2, code, []byte{}, 0, &gas)
		ch <- struct{}{}
	}()
	tick := time.NewTicker(time.Second * 2)
	select {
	case <-tick.C:
		t.Fatal("VM ended up in an infinite loop from bad jump dest (it took too long!)")
	case <-ch:
		if err == nil {
			t.Fatal("Expected invalid jump dest err")
		}
	}
}

// Tests the code for a subcurrency contract compiled by serpent
func TestSubcurrency(t *testing.T) {
	st := newAppState()
	// Create accounts
	account1 := &Account{
		Address: LeftPadWord256(makeBytes(20)),
	}
	account2 := &Account{
		Address: LeftPadWord256(makeBytes(20)),
	}
	st.accounts[account1.Address.String()] = account1
	st.accounts[account2.Address.String()] = account2

	ourVm := NewVM(st, DefaultDynamicMemoryProvider, newParams(), Zero256, nil)

	var gas int64 = 1000
	code_parts := []string{"620f42403355",
		"7c0100000000000000000000000000000000000000000000000000000000",
		"600035046315cf268481141561004657",
		"6004356040526040515460605260206060f35b63693200ce81141561008757",
		"60043560805260243560a052335460c0523360e05260a05160c05112151561008657",
		"60a05160c0510360e0515560a0516080515401608051555b5b505b6000f3"}
	code, _ := hex.DecodeString(strings.Join(code_parts, ""))
	fmt.Printf("Code: %x\n", code)
	data, _ := hex.DecodeString("693200CE0000000000000000000000004B4363CDE27C2EB05E66357DB05BC5C88F850C1A0000000000000000000000000000000000000000000000000000000000000005")
	output, err := ourVm.Call(account1, account2, code, data, 0, &gas)
	fmt.Printf("Output: %v Error: %v\n", output, err)
	if err != nil {
		t.Fatal(err)
	}
}

// Test sending tokens from a contract to another account
func TestSendCall(t *testing.T) {
	fakeAppState := newAppState()
	ourVm := NewVM(fakeAppState, DefaultDynamicMemoryProvider, newParams(), Zero256, nil)

	// Create accounts
	account1 := &Account{
		Address: Int64ToWord256(100),
	}
	account2 := &Account{
		Address: Int64ToWord256(101),
	}
	account3 := &Account{
		Address: Int64ToWord256(102),
	}

	// account1 will call account2 which will trigger CALL opcode to account3
	addr := account3.Address.Postfix(20)
	contractCode := callContractCode(addr)

	//----------------------------------------------
	// account2 has insufficient balance, should fail
	_, err := runVMWaitError(ourVm, account1, account2, addr, contractCode, 10000) // @NOTE: was fixed
	assert.Error(t, err, "Expected insufficient balance error")

	//----------------------------------------------
	// give account2 sufficient balance, should pass
	account2.Balance = 100000
	_, err = runVMWaitError(ourVm, account1, account2, addr, contractCode, 10000) // @NOTE: was fixed
	assert.NoError(t, err, "Should have sufficient balance")

	//----------------------------------------------
	// insufficient gas, should fail

	account2.Balance = 100000
	_, err = runVMWaitError(ourVm, account1, account2, addr, contractCode, 100)
	assert.Error(t, err, "Expected insufficient gas error")
}

// This test was introduced to cover an issues exposed in our handling of the
// gas limit passed from caller to callee on various forms of CALL.
// The idea of this test is to implement a simple DelegateCall in EVM code
// We first run the DELEGATECALL with _just_ enough gas expecting a simple return,
// and then run it with 1 gas unit less, expecting a failure
func TestDelegateCallGas(t *testing.T) {
	appState := newAppState()
	ourVm := NewVM(appState, DefaultDynamicMemoryProvider, newParams(), Zero256, nil)

	inOff := 0
	inSize := 0 // no call data
	retOff := 0
	retSize := 32
	calleeReturnValue := int64(20)

	// DELEGATECALL(retSize, refOffset, inSize, inOffset, addr, gasLimit)
	// 6 pops
	delegateCallCost := GasStackOp * 6
	// 1 push
	gasCost := GasStackOp
	// 2 pops, 1 push
	subCost := GasStackOp * 3
	pushCost := GasStackOp

	costBetweenGasAndDelegateCall := gasCost + subCost + delegateCallCost + pushCost

	// Do a simple operation using 1 gas unit
	calleeAccount, calleeAddress := makeAccountWithCode(appState, "callee",
		Bytecode(PUSH1, calleeReturnValue, return1()))

	// Here we split up the caller code so we can make a DELEGATE call with
	// different amounts of gas. The value we sandwich in the middle is the amount
	// we subtract from the available gas (that the caller has available), so:
	// code := Bytecode(callerCodePrefix, <amount to subtract from GAS> , callerCodeSuffix)
	// gives us the code to make the call
	callerCodePrefix := Bytecode(PUSH1, retSize, PUSH1, retOff, PUSH1, inSize,
		PUSH1, inOff, PUSH20, calleeAddress, PUSH1)
	callerCodeSuffix := Bytecode(GAS, SUB, DELEGATECALL, returnWord())

	// Perform a delegate call
	callerAccount, _ := makeAccountWithCode(appState, "caller",
		Bytecode(callerCodePrefix,
			// Give just enough gas to make the DELEGATECALL
			costBetweenGasAndDelegateCall,
			callerCodeSuffix))

	// Should pass
	output, err := runVMWaitError(ourVm, callerAccount, calleeAccount, calleeAddress,
		callerAccount.Code, 100)
	assert.NoError(t, err, "Should have sufficient funds for call")
	assert.Equal(t, Int64ToWord256(calleeReturnValue).Bytes(), output)

	callerAccount.Code = Bytecode(callerCodePrefix,
		// Shouldn't be enough gas to make call
		costBetweenGasAndDelegateCall-1,
		callerCodeSuffix)

	// Should fail
	_, err = runVMWaitError(ourVm, callerAccount, calleeAccount, calleeAddress,
		callerAccount.Code, 100)
	assert.Error(t, err, "Should have insufficient funds for call")
}

func TestMemoryBounds(t *testing.T) {
	appState := newAppState()
	memoryProvider := func() Memory {
		return NewDynamicMemory(1024, 2048)
	}
	ourVm := NewVM(appState, memoryProvider, newParams(), Zero256, nil)
	caller, _ := makeAccountWithCode(appState, "caller", nil)
	callee, _ := makeAccountWithCode(appState, "callee", nil)
	gas := int64(100000)
	// This attempts to store a value at the memory boundary and return it
	word := One256
	output, err := ourVm.call(caller, callee,
		Bytecode(pushWord(word), storeAtEnd(), MLOAD, storeAtEnd(), returnAfterStore()),
		nil, 0, &gas)
	assert.NoError(t, err)
	assert.Equal(t, word.Bytes(), output)

	// Same with number
	word = Int64ToWord256(232234234432)
	output, err = ourVm.call(caller, callee,
		Bytecode(pushWord(word), storeAtEnd(), MLOAD, storeAtEnd(), returnAfterStore()),
		nil, 0, &gas)
	assert.NoError(t, err)
	assert.Equal(t, word.Bytes(), output)

	// Now test a series of boundary stores
	code := pushWord(word)
	for i := 0; i < 10; i++ {
		code = Bytecode(code, storeAtEnd(), MLOAD)
	}
	output, err = ourVm.call(caller, callee, Bytecode(code, storeAtEnd(), returnAfterStore()),
		nil, 0, &gas)
	assert.NoError(t, err)
	assert.Equal(t, word.Bytes(), output)

	// Same as above but we should breach the upper memory limit set in memoryProvider
	code = pushWord(word)
	for i := 0; i < 100; i++ {
		code = Bytecode(code, storeAtEnd(), MLOAD)
	}
	output, err = ourVm.call(caller, callee, Bytecode(code, storeAtEnd(), returnAfterStore()),
		nil, 0, &gas)
	assert.Error(t, err, "Should hit memory out of bounds")
}

// These code segment helpers exercise the MSTORE MLOAD MSTORE cycle to test
// both of the memory operations. Each MSTORE is done on the memory boundary
// (at MSIZE) which Solidity uses to find guaranteed unallocated memory.

// storeAtEnd expects the value to be stored to be on top of the stack, it then
// stores that value at the current memory boundary
func storeAtEnd() []byte {
	// Pull in MSIZE (to carry forward to MLOAD), swap in value to store, store it at MSIZE
	return Bytecode(MSIZE, SWAP1, DUP2, MSTORE)
}

func returnAfterStore() []byte {
	return Bytecode(PUSH1, 32, DUP2, RETURN)
}

// Store the top element of the stack (which is a 32-byte word) in memory
// and return it. Useful for a simple return value.
func return1() []byte {
	return Bytecode(PUSH1, 0, MSTORE, returnWord())
}

func returnWord() []byte {
	// PUSH1 => return size, PUSH1 => return offset, RETURN
	return Bytecode(PUSH1, 32, PUSH1, 0, RETURN)
}

func makeAccountWithCode(appState AppState, name string,
	code []byte) (*Account, []byte) {
	account := &Account{
		Address: LeftPadWord256([]byte(name)),
		Balance: 9999999,
		Code:    code,
		Nonce:   0,
	}
	account.Code = code
	appState.UpdateAccount(account)
	// Sanity check
	address := new([20]byte)
	for i, b := range account.Address.Postfix(20) {
		address[i] = b
	}
	return account, address[:]
}

// Subscribes to an AccCall, runs the vm, returns the output any direct exception
// and then waits for any exceptions transmitted by EventData in the AccCall
// event (in the case of no direct error from call we will block waiting for
// at least 1 AccCall event)
func runVMWaitError(ourVm *VM, caller, callee *Account, subscribeAddr,
	contractCode []byte, gas int64) (output []byte, err error) {
	eventCh := make(chan txs.EventData)
	output, err = runVM(eventCh, ourVm, caller, callee, subscribeAddr,
		contractCode, gas)
	if err != nil {
		return
	}
	msg := <-eventCh
	var errString string
	switch ev := msg.(type) {
	case txs.EventDataTx:
		errString = ev.Exception
	case txs.EventDataCall:
		errString = ev.Exception
	}

	if errString != "" {
		err = errors.New(errString)
	}
	return
}

// Subscribes to an AccCall, runs the vm, returns the output and any direct
// exception
func runVM(eventCh chan txs.EventData, ourVm *VM, caller, callee *Account,
	subscribeAddr, contractCode []byte, gas int64) ([]byte, error) {

	// we need to catch the event from the CALL to check for exceptions
	evsw := events.NewEventSwitch()
	evsw.Start()
	fmt.Printf("subscribe to %x\n", subscribeAddr)
	evsw.AddListenerForEvent("test", txs.EventStringAccCall(subscribeAddr),
		func(msg events.EventData) {
			eventCh <- msg.(txs.EventData)
		})
	evc := events.NewEventCache(evsw)
	ourVm.SetFireable(evc)
	start := time.Now()
	output, err := ourVm.Call(caller, callee, contractCode, []byte{}, 0, &gas)
	fmt.Printf("Output: %v Error: %v\n", output, err)
	fmt.Println("Call took:", time.Since(start))
	go func() { evc.Flush() }()
	return output, err
}

// this is code to call another contract (hardcoded as addr)
func callContractCode(addr []byte) []byte {
	gas1, gas2 := byte(0x1), byte(0x1)
	value := byte(0x69)
	inOff, inSize := byte(0x0), byte(0x0) // no call data
	retOff, retSize := byte(0x0), byte(0x20)
	// this is the code we want to run (send funds to an account and return)
	return Bytecode(PUSH1, retSize, PUSH1, retOff, PUSH1, inSize, PUSH1,
		inOff, PUSH1, value, PUSH20, addr, PUSH2, gas1, gas2, CALL, PUSH1, retSize,
		PUSH1, retOff, RETURN)
}

func pushInt64(i int64) []byte {
	return pushWord(Int64ToWord256(i))
}

// Produce bytecode for a PUSH<N>, b_1, ..., b_N where the N is number of bytes
// contained in the unpadded word
func pushWord(word Word256) []byte {
	leadingZeros := byte(0)
	for leadingZeros < 32 {
		if word[leadingZeros] == 0 {
			leadingZeros++
		} else {
			return Bytecode(byte(PUSH32)-leadingZeros, word[leadingZeros:])
		}
	}
	return Bytecode(PUSH1, 0)
}

func TestPushWord(t *testing.T) {
	word := Int64ToWord256(int64(2133213213))
	assert.Equal(t, Bytecode(PUSH4, 0x7F, 0x26, 0x40, 0x1D), pushWord(word))
	word[0] = 1
	assert.Equal(t, Bytecode(PUSH32,
		1, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x7F, 0x26, 0x40, 0x1D), pushWord(word))
	assert.Equal(t, Bytecode(PUSH1, 0), pushWord(Word256{}))
	assert.Equal(t, Bytecode(PUSH1, 1), pushWord(Int64ToWord256(1)))
}

func TestBytecode(t *testing.T) {
	assert.Equal(t,
		Bytecode(1, 2, 3, 4, 5, 6),
		Bytecode(1, 2, 3, Bytecode(4, 5, 6)))
	assert.Equal(t,
		Bytecode(1, 2, 3, 4, 5, 6, 7, 8),
		Bytecode(1, 2, 3, Bytecode(4, Bytecode(5), 6), 7, 8))
	assert.Equal(t,
		Bytecode(PUSH1, 2),
		Bytecode(byte(PUSH1), 0x02))
	assert.Equal(t,
		[]byte{},
		Bytecode(Bytecode(Bytecode())))

	contractAccount := &Account{Address: Int64ToWord256(102)}
	addr := contractAccount.Address.Postfix(20)
	gas1, gas2 := byte(0x1), byte(0x1)
	value := byte(0x69)
	inOff, inSize := byte(0x0), byte(0x0) // no call data
	retOff, retSize := byte(0x0), byte(0x20)
	contractCodeBytecode := Bytecode(PUSH1, retSize, PUSH1, retOff, PUSH1, inSize, PUSH1,
		inOff, PUSH1, value, PUSH20, addr, PUSH2, gas1, gas2, CALL, PUSH1, retSize,
		PUSH1, retOff, RETURN)
	contractCode := []byte{0x60, retSize, 0x60, retOff, 0x60, inSize, 0x60, inOff, 0x60, value, 0x73}
	contractCode = append(contractCode, addr...)
	contractCode = append(contractCode, []byte{0x61, gas1, gas2, 0xf1, 0x60, 0x20, 0x60, 0x0, 0xf3}...)
	assert.Equal(t, contractCode, contractCodeBytecode)
}

func TestConcat(t *testing.T) {
	assert.Equal(t,
		[]byte{0x01, 0x02, 0x03, 0x04},
		Concat([]byte{0x01, 0x02}, []byte{0x03, 0x04}))
}
