package rest

import (
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"

	restful "github.com/emicklei/go-restful"

	"github.com/derekparker/delve/service"
	"github.com/derekparker/delve/service/api"
	"github.com/derekparker/delve/service/debugger"
)

// RESTServer exposes a Debugger via a HTTP REST API.
type RESTServer struct {
	// config is all the information necessary to start the debugger and server.
	config *service.Config
	// listener is used to serve HTTP.
	listener net.Listener
	// debugger is a debugger service.
	debugger *debugger.Debugger
}

// NewServer creates a new RESTServer.
func NewServer(config *service.Config, logEnabled bool) *RESTServer {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if !logEnabled {
		log.SetOutput(ioutil.Discard)
	}

	return &RESTServer{
		config:   config,
		listener: config.Listener,
	}
}

// Run starts a debugger and exposes it with an HTTP server. The debugger
// itself can be stopped with the `detach` API. Run blocks until the HTTP
// server stops.
func (s *RESTServer) Run() error {
	var err error
	// Create and start the debugger
	if s.debugger, err = debugger.New(&debugger.Config{
		ProcessArgs: s.config.ProcessArgs,
		AttachPid:   s.config.AttachPid,
	}); err != nil {
		return err
	}

	// Set up the HTTP server
	container := restful.NewContainer()
	ws := new(restful.WebService)
	ws.
		Path("").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON).
		Route(ws.GET("/state").To(s.getState)).
		Route(ws.GET("/breakpoints").To(s.listBreakpoints)).
		Route(ws.GET("/breakpoints/{breakpoint-id}").To(s.getBreakpoint)).
		Route(ws.POST("/breakpoints").To(s.createBreakpoint)).
		Route(ws.DELETE("/breakpoints/{breakpoint-id}").To(s.clearBreakpoint)).
		Route(ws.GET("/threads").To(s.listThreads)).
		Route(ws.GET("/threads/{thread-id}").To(s.getThread)).
		Route(ws.GET("/threads/{thread-id}/vars").To(s.listThreadPackageVars)).
		Route(ws.GET("/threads/{thread-id}/eval/{symbol}").To(s.evalThreadSymbol)).
		Route(ws.GET("/goroutines").To(s.listGoroutines)).
		Route(ws.GET("/goroutines/{goroutine-id}/trace").To(s.stacktraceGoroutine)).
		Route(ws.POST("/command").To(s.doCommand)).
		Route(ws.GET("/sources").To(s.listSources)).
		Route(ws.GET("/functions").To(s.listFunctions)).
		Route(ws.GET("/regs").To(s.listRegisters)).
		Route(ws.GET("/vars").To(s.listPackageVars)).
		Route(ws.GET("/localvars").To(s.listLocalVars)).
		Route(ws.GET("/args").To(s.listFunctionArgs)).
		Route(ws.GET("/eval/{symbol}").To(s.evalSymbol)).
		// TODO: GET might be the wrong verb for this
		Route(ws.GET("/detach").To(s.detach))
	container.Add(ws)

	// Start the HTTP server
	log.Printf("server listening on %s", s.listener.Addr())
	return http.Serve(s.listener, container)
}

// Stop detaches from the debugger and waits for it to stop.
func (s *RESTServer) Stop(kill bool) error {
	return s.debugger.Detach(kill)
}

// writeError writes a simple error response.
func writeError(response *restful.Response, statusCode int, message string) {
	response.AddHeader("Content-Type", "text/plain")
	response.WriteErrorString(statusCode, message)
}

// detach stops the debugger and waits for it to shut down before returning an
// OK response. Clients expect this to be a synchronous call.
func (s *RESTServer) detach(request *restful.Request, response *restful.Response) {
	kill, err := strconv.ParseBool(request.QueryParameter("kill"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid kill parameter")
		return
	}

	err = s.Stop(kill)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
}

func (s *RESTServer) getState(request *restful.Request, response *restful.Response) {
	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteEntity(state)
}

func (s *RESTServer) doCommand(request *restful.Request, response *restful.Response) {
	command := new(api.DebuggerCommand)
	err := request.ReadEntity(command)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	state, err := s.debugger.Command(command)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusCreated)
	response.WriteEntity(state)
}

func (s *RESTServer) getBreakpoint(request *restful.Request, response *restful.Response) {
	id, err := strconv.Atoi(request.PathParameter("breakpoint-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid breakpoint id")
		return
	}

	found := s.debugger.FindBreakpoint(id)
	if found == nil {
		writeError(response, http.StatusNotFound, "breakpoint not found")
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(found)
}

func (s *RESTServer) stacktraceGoroutine(request *restful.Request, response *restful.Response) {
	goroutineId, err := strconv.Atoi(request.PathParameter("goroutine-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid goroutine id")
		return
	}

	depth, err := strconv.Atoi(request.QueryParameter("depth"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid depth")
		return
	}

	locations, err := s.debugger.Stacktrace(goroutineId, depth)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(locations)
}

func (s *RESTServer) listBreakpoints(request *restful.Request, response *restful.Response) {
	response.WriteEntity(s.debugger.Breakpoints())
}

func (s *RESTServer) createBreakpoint(request *restful.Request, response *restful.Response) {
	incomingBp := new(api.Breakpoint)
	err := request.ReadEntity(incomingBp)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	if len(incomingBp.File) == 0 && len(incomingBp.FunctionName) == 0 {
		writeError(response, http.StatusBadRequest, "no file or function name provided")
		return
	}

	createdbp, err := s.debugger.CreateBreakpoint(incomingBp)

	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusCreated)
	response.WriteEntity(createdbp)
}

func (s *RESTServer) clearBreakpoint(request *restful.Request, response *restful.Response) {
	id, err := strconv.Atoi(request.PathParameter("breakpoint-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid breakpoint id")
		return
	}

	found := s.debugger.FindBreakpoint(id)
	if found == nil {
		writeError(response, http.StatusNotFound, "breakpoint not found")
		return
	}

	deleted, err := s.debugger.ClearBreakpoint(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(deleted)
}

func (s *RESTServer) listThreads(request *restful.Request, response *restful.Response) {
	response.WriteEntity(s.debugger.Threads())
}

func (s *RESTServer) getThread(request *restful.Request, response *restful.Response) {
	id, err := strconv.Atoi(request.PathParameter("thread-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid thread id")
		return
	}

	found := s.debugger.FindThread(id)
	if found == nil {
		writeError(response, http.StatusNotFound, "thread not found")
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(found)
}

func (s *RESTServer) listPackageVars(request *restful.Request, response *restful.Response) {
	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	current := state.CurrentThread
	if current == nil {
		writeError(response, http.StatusBadRequest, "no current thread")
		return
	}

	filter := request.QueryParameter("filter")
	vars, err := s.debugger.PackageVariables(current.ID, filter)

	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(vars)
}

func (s *RESTServer) listThreadPackageVars(request *restful.Request, response *restful.Response) {
	id, err := strconv.Atoi(request.PathParameter("thread-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid thread id")
		return
	}

	if found := s.debugger.FindThread(id); found == nil {
		writeError(response, http.StatusNotFound, "thread not found")
		return
	}

	filter := request.QueryParameter("filter")
	vars, err := s.debugger.PackageVariables(id, filter)

	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(vars)
}

func (s *RESTServer) listRegisters(request *restful.Request, response *restful.Response) {
	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	regs, err := s.debugger.Registers(state.CurrentThread.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(regs)
}

func (s *RESTServer) listLocalVars(request *restful.Request, response *restful.Response) {
	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	vars, err := s.debugger.LocalVariables(state.CurrentThread.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(vars)
}

func (s *RESTServer) listFunctionArgs(request *restful.Request, response *restful.Response) {
	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	vars, err := s.debugger.FunctionArguments(state.CurrentThread.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(vars)
}

func (s *RESTServer) evalSymbol(request *restful.Request, response *restful.Response) {
	symbol := request.PathParameter("symbol")
	if len(symbol) == 0 {
		writeError(response, http.StatusBadRequest, "invalid symbol")
		return
	}

	state, err := s.debugger.State()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	current := state.CurrentThread
	if current == nil {
		writeError(response, http.StatusBadRequest, "no current thread")
		return
	}

	v, err := s.debugger.EvalVariableInThread(current.ID, symbol)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(v)
}

func (s *RESTServer) evalThreadSymbol(request *restful.Request, response *restful.Response) {
	id, err := strconv.Atoi(request.PathParameter("thread-id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid thread id")
		return
	}

	if found := s.debugger.FindThread(id); found == nil {
		writeError(response, http.StatusNotFound, "thread not found")
		return
	}

	symbol := request.PathParameter("symbol")
	if len(symbol) == 0 {
		writeError(response, http.StatusNotFound, "invalid symbol")
		return
	}

	v, err := s.debugger.EvalVariableInThread(id, symbol)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(v)
}

func (s *RESTServer) listSources(request *restful.Request, response *restful.Response) {
	filter := request.QueryParameter("filter")
	sources, err := s.debugger.Sources(filter)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(sources)
}

func (s *RESTServer) listFunctions(request *restful.Request, response *restful.Response) {
	filter := request.QueryParameter("filter")
	funcs, err := s.debugger.Functions(filter)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}

	response.WriteHeader(http.StatusOK)
	response.WriteEntity(funcs)
}

func (s *RESTServer) listGoroutines(request *restful.Request, response *restful.Response) {
	gs, err := s.debugger.Goroutines()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	response.WriteHeader(http.StatusOK)
	response.WriteEntity(gs)
}
