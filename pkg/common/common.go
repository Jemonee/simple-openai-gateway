package common

type PageQuery[T any, E any] struct {
	Page  Page[E] `json:"page"`
	Query T       `json:"query"`
}

type Page[T any] struct {
	PageSize int   `json:"pageSize"`
	PageNo   int   `json:"pageNo"`
	Result   []T   `json:"result"`
	Total    int64 `json:"total"`
}

func (p Page[T]) GetFirst() int {
	return (p.PageNo - 1) * p.PageSize
}

type ServicePanic struct {
	Code int
	Msg  string
}

type R[T any] struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Data    *T     `json:"data"`
	Message string `json:"message"`
}

func S[T any](data *T) R[T] {
	r := R[T]{}
	r.Code = 200
	r.Success = true
	r.Message = "操作成功!"
	if data != nil {
		r.Data = data
	}
	return r
}

func F[T any](code int, message string) R[T] {
	r := R[T]{}
	r.Code = code
	r.Success = false
	r.Message = message
	r.Data = nil
	return r
}
