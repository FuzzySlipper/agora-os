cmd/                                                                                                                                                                          
    isolation-service/main.go   — Unix socket server, dispatches spawn/terminate/list                                                                                           
    admin-agent/main.go         — stateless LLM evaluator, append-only logging                                                                                                  
    audit-service/main.go       — fanotify watcher, uid attribution, structured events                                                                                          
  internal/                                                                                                                                                                     
    schema/schema.go            — shared types: requests, responses, wire protocol                                                                                              
    agent/agent.go              — agent lifecycle: useradd, cgroup slices, nftables rules                                                                                       
  config/                                                                                                                                                                       
    admin-agent-system-prompt.md — the out-of-band prompt (the only instruction channel)                                                                                        
                                                                                                                                                                                
  Key design decisions in the sketch:                                                                                                                                           
                                                                                                                                                                                
  - Wire protocol: all three services use the same {method, body} / {ok, body} JSON envelope over Unix sockets. Simple enough to test with socat or a quick Go client, no       
  dependency on gRPC yet.                                                                                                                                                       
  - Admin agent failure mode: if the LLM call fails or returns unparseable JSON, the decision defaults to escalate (human review), never approve. That asymmetric risk bias from
   your research doc is baked in.                                                                                                                                               
  - Fanotify over inotify: uses FAN_MARK_FILESYSTEM to watch an entire filesystem subtree rather than registering individual directories. Gets uid attribution from             
  /proc/<pid>/status since fanotify gives you the pid. Raw syscall wrappers since there's no good Go fanotify library — that's maybe 50 lines of code that wraps three syscalls.
  - Agent uids start at 60000: avoids collision with system users and normal users. Pool of 1000 is arbitrary but plenty for v1.                                                
  - nftables rules use meta skuid: matches outbound packets by the uid of the socket owner. The kernel does the filtering, no userspace proxy needed.                           
                                                                                                                                                                                
  What's missing that you'd add next:                                                                                                                                           
                                                                                                                                                                                
  - A CLI client to actually talk to the sockets (a quick cmd/agentctl/main.go that sends JSON and prints the response)                                                         
  - The nftables chain setup — the rules assume an inet filter output chain exists; you'd need an init script or the isolation service to create it on startup                  
  - Agent process execution — right now Spawn creates the user but doesn't run anything as that user. You'd add a systemd-run --uid call or similar to actually start agent     
  workloads in the slice                                                                                                                                                        
  - Tests, but for a sketch the test is "run it and poke the socket with socat"# agora-os
