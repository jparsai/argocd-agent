// Code generated by mockery v2.43.0. DO NOT EDIT.

package mocks

import (
	issuer "github.com/jannfis/argocd-agent/internal/issuer"
	mock "github.com/stretchr/testify/mock"

	time "time"
)

// Issuer is an autogenerated mock type for the Issuer type
type Issuer struct {
	mock.Mock
}

type Issuer_Expecter struct {
	mock *mock.Mock
}

func (_m *Issuer) EXPECT() *Issuer_Expecter {
	return &Issuer_Expecter{mock: &_m.Mock}
}

// IssueAccessToken provides a mock function with given fields: client, exp
func (_m *Issuer) IssueAccessToken(client string, exp time.Duration) (string, error) {
	ret := _m.Called(client, exp)

	if len(ret) == 0 {
		panic("no return value specified for IssueAccessToken")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(string, time.Duration) (string, error)); ok {
		return rf(client, exp)
	}
	if rf, ok := ret.Get(0).(func(string, time.Duration) string); ok {
		r0 = rf(client, exp)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(string, time.Duration) error); ok {
		r1 = rf(client, exp)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Issuer_IssueAccessToken_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'IssueAccessToken'
type Issuer_IssueAccessToken_Call struct {
	*mock.Call
}

// IssueAccessToken is a helper method to define mock.On call
//   - client string
//   - exp time.Duration
func (_e *Issuer_Expecter) IssueAccessToken(client interface{}, exp interface{}) *Issuer_IssueAccessToken_Call {
	return &Issuer_IssueAccessToken_Call{Call: _e.mock.On("IssueAccessToken", client, exp)}
}

func (_c *Issuer_IssueAccessToken_Call) Run(run func(client string, exp time.Duration)) *Issuer_IssueAccessToken_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(time.Duration))
	})
	return _c
}

func (_c *Issuer_IssueAccessToken_Call) Return(_a0 string, _a1 error) *Issuer_IssueAccessToken_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *Issuer_IssueAccessToken_Call) RunAndReturn(run func(string, time.Duration) (string, error)) *Issuer_IssueAccessToken_Call {
	_c.Call.Return(run)
	return _c
}

// IssueRefreshToken provides a mock function with given fields: client, exp
func (_m *Issuer) IssueRefreshToken(client string, exp time.Duration) (string, error) {
	ret := _m.Called(client, exp)

	if len(ret) == 0 {
		panic("no return value specified for IssueRefreshToken")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(string, time.Duration) (string, error)); ok {
		return rf(client, exp)
	}
	if rf, ok := ret.Get(0).(func(string, time.Duration) string); ok {
		r0 = rf(client, exp)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(string, time.Duration) error); ok {
		r1 = rf(client, exp)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Issuer_IssueRefreshToken_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'IssueRefreshToken'
type Issuer_IssueRefreshToken_Call struct {
	*mock.Call
}

// IssueRefreshToken is a helper method to define mock.On call
//   - client string
//   - exp time.Duration
func (_e *Issuer_Expecter) IssueRefreshToken(client interface{}, exp interface{}) *Issuer_IssueRefreshToken_Call {
	return &Issuer_IssueRefreshToken_Call{Call: _e.mock.On("IssueRefreshToken", client, exp)}
}

func (_c *Issuer_IssueRefreshToken_Call) Run(run func(client string, exp time.Duration)) *Issuer_IssueRefreshToken_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(time.Duration))
	})
	return _c
}

func (_c *Issuer_IssueRefreshToken_Call) Return(_a0 string, _a1 error) *Issuer_IssueRefreshToken_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *Issuer_IssueRefreshToken_Call) RunAndReturn(run func(string, time.Duration) (string, error)) *Issuer_IssueRefreshToken_Call {
	_c.Call.Return(run)
	return _c
}

// ValidateAccessToken provides a mock function with given fields: token
func (_m *Issuer) ValidateAccessToken(token string) (issuer.Claims, error) {
	ret := _m.Called(token)

	if len(ret) == 0 {
		panic("no return value specified for ValidateAccessToken")
	}

	var r0 issuer.Claims
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (issuer.Claims, error)); ok {
		return rf(token)
	}
	if rf, ok := ret.Get(0).(func(string) issuer.Claims); ok {
		r0 = rf(token)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(issuer.Claims)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(token)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Issuer_ValidateAccessToken_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ValidateAccessToken'
type Issuer_ValidateAccessToken_Call struct {
	*mock.Call
}

// ValidateAccessToken is a helper method to define mock.On call
//   - token string
func (_e *Issuer_Expecter) ValidateAccessToken(token interface{}) *Issuer_ValidateAccessToken_Call {
	return &Issuer_ValidateAccessToken_Call{Call: _e.mock.On("ValidateAccessToken", token)}
}

func (_c *Issuer_ValidateAccessToken_Call) Run(run func(token string)) *Issuer_ValidateAccessToken_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *Issuer_ValidateAccessToken_Call) Return(_a0 issuer.Claims, _a1 error) *Issuer_ValidateAccessToken_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *Issuer_ValidateAccessToken_Call) RunAndReturn(run func(string) (issuer.Claims, error)) *Issuer_ValidateAccessToken_Call {
	_c.Call.Return(run)
	return _c
}

// ValidateRefreshToken provides a mock function with given fields: token
func (_m *Issuer) ValidateRefreshToken(token string) (issuer.Claims, error) {
	ret := _m.Called(token)

	if len(ret) == 0 {
		panic("no return value specified for ValidateRefreshToken")
	}

	var r0 issuer.Claims
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (issuer.Claims, error)); ok {
		return rf(token)
	}
	if rf, ok := ret.Get(0).(func(string) issuer.Claims); ok {
		r0 = rf(token)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(issuer.Claims)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(token)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Issuer_ValidateRefreshToken_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ValidateRefreshToken'
type Issuer_ValidateRefreshToken_Call struct {
	*mock.Call
}

// ValidateRefreshToken is a helper method to define mock.On call
//   - token string
func (_e *Issuer_Expecter) ValidateRefreshToken(token interface{}) *Issuer_ValidateRefreshToken_Call {
	return &Issuer_ValidateRefreshToken_Call{Call: _e.mock.On("ValidateRefreshToken", token)}
}

func (_c *Issuer_ValidateRefreshToken_Call) Run(run func(token string)) *Issuer_ValidateRefreshToken_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *Issuer_ValidateRefreshToken_Call) Return(_a0 issuer.Claims, _a1 error) *Issuer_ValidateRefreshToken_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *Issuer_ValidateRefreshToken_Call) RunAndReturn(run func(string) (issuer.Claims, error)) *Issuer_ValidateRefreshToken_Call {
	_c.Call.Return(run)
	return _c
}

// NewIssuer creates a new instance of Issuer. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewIssuer(t interface {
	mock.TestingT
	Cleanup(func())
}) *Issuer {
	mock := &Issuer{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
