package web

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// DevPluginAudio is the narrow audio-core surface behind the development-only
// plugin panel. It deliberately stays outside the public /api/synth path: these
// calls poke active CLAP plugins by host parameter id and are meant for local
// integration/debugging, not as a stable editor API.
type DevPluginAudio interface {
	DiscoverClapParams(bundlePath, pluginID string) ([]audio.ClapParamInfo, error)
	SetClapParam(clapID uint32, value float64) error
	Metrics() audio.Metrics
}

type realDevPluginAudio struct{}

func (realDevPluginAudio) DiscoverClapParams(bundlePath, pluginID string) ([]audio.ClapParamInfo, error) {
	return audio.DiscoverClapParams(bundlePath, pluginID)
}
func (realDevPluginAudio) SetClapParam(clapID uint32, value float64) error {
	return audio.SetClapParam(clapID, value)
}
func (realDevPluginAudio) Metrics() audio.Metrics { return audio.GetMetrics() }

type pluginRegistryStatus interface {
	Active() *patches.Patch
	Status() patches.LoadStatus
}

func devWebEnabled() bool { return os.Getenv("POLYCLAV_DEV_WEB") == "1" }

func (s *Server) routesDevPlugin() {
	if !devWebEnabled() {
		return
	}
	if s.deps.DevPluginAudio == nil {
		s.deps.DevPluginAudio = realDevPluginAudio{}
	}
	s.mux.HandleFunc("GET /dev/plugin", s.handleDevPluginPage)
	s.mux.HandleFunc("GET /api/dev/plugin/status", s.handleDevPluginStatus)
	s.mux.HandleFunc("GET /api/dev/plugin/params", s.handleDevPluginParams)
	s.mux.HandleFunc("PATCH /api/dev/plugin/params", s.handleDevPluginParamPatch)
}

func (s *Server) devAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !devWebEnabled() {
		http.NotFound(w, r)
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		writeErr(w, http.StatusForbidden, "development plugin panel is loopback-only")
		return false
	}
	return true
}

type devPluginStatusJSON struct {
	CurrentPatch string                   `json:"current_patch"`
	ActivePatch  string                   `json:"active_patch"`
	PatchType    string                   `json:"patch_type"`
	Requested    devPluginRequestedJSON   `json:"requested"`
	Load         devPluginLoadJSON        `json:"load"`
	Binding      devPluginBindingJSON     `json:"binding"`
	Metrics      devPluginMetricsJSON     `json:"metrics"`
	OrganParams  []devPluginOrganParamRef `json:"organ_params"`
}

type devPluginRequestedJSON struct {
	PluginPath string `json:"plugin_path,omitempty"`
	PluginID   string `json:"plugin_id,omitempty"`
}

type devPluginLoadJSON struct {
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
	LastError  string `json:"last_error"`
}

type devPluginBindingJSON struct {
	Enabled         bool     `json:"enabled"`
	Ownership       string   `json:"ownership"`
	DrawbarParamIDs []string `json:"drawbar_param_ids"`
	DrawbarClapIDs  []uint32 `json:"drawbar_clap_ids"`
}

type devPluginMetricsJSON struct {
	BackendSwaps        uint64 `json:"backend_swaps"`
	PluginRenderErrors  uint64 `json:"plugin_render_errors"`
	ClapInputEventDrops uint64 `json:"clap_input_event_drops"`
}

type devPluginOrganParamRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var potatoOrganParamLabels = []devPluginOrganParamRef{
	{ID: "drawbar16", Label: "16′ drawbar"},
	{ID: "drawbar5_1_3", Label: "5⅓′ drawbar"},
	{ID: "drawbar8", Label: "8′ drawbar"},
	{ID: "drawbar4", Label: "4′ drawbar"},
	{ID: "drawbar2_2_3", Label: "2⅔′ drawbar"},
	{ID: "drawbar2", Label: "2′ drawbar"},
	{ID: "drawbar1_3_5", Label: "1⅗′ drawbar"},
	{ID: "drawbar1_1_3", Label: "1⅓′ drawbar"},
	{ID: "drawbar1", Label: "1′ drawbar"},
	{ID: "expression", Label: "Expression"},
	{ID: "rotaryMode", Label: "Rotary mode"},
	{ID: "percussionMode", Label: "Percussion mode"},
	{ID: "click", Label: "Key click"},
	{ID: "percussionLevel", Label: "Percussion level"},
	{ID: "percussionDecay", Label: "Percussion decay"},
	{ID: "rotaryWidth", Label: "Rotary width"},
	{ID: "drive", Label: "Drive"},
	{ID: "tone", Label: "Tone"},
	{ID: "outputLevel", Label: "Output level"},
	{ID: "factoryPreset", Label: "Factory registration"},
}

func (s *Server) devPluginStatus() devPluginStatusJSON {
	cur := s.deps.Registry.Current()
	active := cur
	load := patches.LoadStatus{Index: -1, State: patches.LoadStateIdle}
	if rs, ok := s.deps.Registry.(pluginRegistryStatus); ok {
		load = rs.Status()
		if p := rs.Active(); p != nil {
			active = p
		}
	}
	out := devPluginStatusJSON{
		Load:        devPluginLoadJSON{Generation: load.Generation, State: string(load.State), LastError: load.Err},
		OrganParams: potatoOrganParamLabels,
	}
	if cur != nil {
		out.CurrentPatch = cur.Name
		out.PatchType = normalizedPatchType(cur.Type)
		out.Requested = devPluginRequestedJSON{PluginPath: cur.PluginPath, PluginID: cur.PluginID}
		out.Binding = devPluginBindingJSON{
			Enabled:         cur.LaunchkeyOrgan.Enabled,
			Ownership:       cur.LaunchkeyOrgan.Ownership,
			DrawbarParamIDs: append([]string(nil), cur.LaunchkeyOrgan.DrawbarParamIDs...),
			DrawbarClapIDs:  append([]uint32(nil), cur.LaunchkeyOrgan.DrawbarClapIDs...),
		}
	}
	if active != nil {
		out.ActivePatch = active.Name
	}
	if s.deps.DevPluginAudio != nil {
		m := s.deps.DevPluginAudio.Metrics()
		out.Metrics = devPluginMetricsJSON{
			BackendSwaps:        m.BackendSwaps,
			PluginRenderErrors:  m.PluginRenderErrors,
			ClapInputEventDrops: m.ClapInputEventDrops,
		}
	}
	return out
}

func normalizedPatchType(typ string) string {
	if typ == "" {
		return "soundfont"
	}
	return typ
}

func (s *Server) handleDevPluginStatus(w http.ResponseWriter, r *http.Request) {
	if !s.devAllowed(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.devPluginStatus())
}

type devPluginParamJSON struct {
	ID      string  `json:"id"`
	ClapID  uint32  `json:"clap_id"`
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Module  string  `json:"module,omitempty"`
	Value   float64 `json:"value"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Default float64 `json:"default"`
	Flags   uint32  `json:"flags"`
}

func (s *Server) activeDevClapPatch() (*patches.Patch, error) {
	cur := s.deps.Registry.Current()
	if cur == nil || normalizedPatchType(cur.Type) != "clap" {
		return nil, fmt.Errorf("no CLAP patch selected")
	}
	if rs, ok := s.deps.Registry.(pluginRegistryStatus); ok {
		status := rs.Status()
		active := rs.Active()
		if active == nil || active.Name != cur.Name || status.State != patches.LoadStateActive {
			return nil, fmt.Errorf("CLAP patch is not active yet")
		}
		return active, nil
	}
	return cur, nil
}

func (s *Server) devPluginParams() ([]devPluginParamJSON, error) {
	cur, err := s.activeDevClapPatch()
	if err != nil {
		return nil, err
	}
	if s.deps.DevPluginAudio == nil {
		return nil, fmt.Errorf("development plugin audio API not available")
	}
	var ps []audio.ClapParamInfo
	if s.deps.ClapParams != nil {
		ps = s.deps.ClapParams.All()
	}
	if len(ps) == 0 {
		var err error
		ps, err = s.deps.DevPluginAudio.DiscoverClapParams(cur.PluginPath, cur.PluginID)
		if err != nil {
			return nil, err
		}
		if s.deps.ClapParams != nil {
			s.deps.ClapParams.Replace(ps)
		}
	}
	out := make([]devPluginParamJSON, 0, len(ps))
	for _, p := range ps {
		id := strconv.FormatUint(uint64(p.ClapID), 10)
		out = append(out, devPluginParamJSON{
			ID:      id,
			ClapID:  p.ClapID,
			Name:    p.Name,
			Label:   labelForPluginParam(p),
			Module:  p.Module,
			Value:   p.CurrentValue,
			Min:     p.MinValue,
			Max:     p.MaxValue,
			Default: p.DefaultValue,
			Flags:   p.Flags,
		})
	}
	return out, nil
}

func labelForPluginParam(p audio.ClapParamInfo) string {
	if p.Name == "" {
		return strconv.FormatUint(uint64(p.ClapID), 10)
	}
	return p.Name
}

func (s *Server) handleDevPluginParams(w http.ResponseWriter, r *http.Request) {
	if !s.devAllowed(w, r) {
		return
	}
	ps, err := s.devPluginParams()
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

type devPluginParamPatchBody struct {
	ID    string   `json:"id"`
	Value *float64 `json:"value"`
}

func (s *Server) handleDevPluginParamPatch(w http.ResponseWriter, r *http.Request) {
	if !s.devAllowed(w, r) {
		return
	}
	var body devPluginParamPatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	body.ID = strings.TrimSpace(body.ID)
	if body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if body.Value == nil || math.IsNaN(*body.Value) || math.IsInf(*body.Value, 0) {
		writeErr(w, http.StatusBadRequest, "value must be finite")
		return
	}
	params, err := s.devPluginParams()
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	var match *devPluginParamJSON
	for i := range params {
		if params[i].ID == body.ID || strconv.FormatUint(uint64(params[i].ClapID), 10) == body.ID {
			match = &params[i]
			break
		}
	}
	if match == nil {
		writeErr(w, http.StatusBadRequest, "unknown CLAP parameter id "+body.ID)
		return
	}
	if *body.Value < match.Min || *body.Value > match.Max {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("value must be in [%g,%g]", match.Min, match.Max))
		return
	}
	if err := s.deps.DevPluginAudio.SetClapParam(match.ClapID, *body.Value); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if s.deps.ClapParams != nil {
		s.deps.ClapParams.Update(match.ClapID, *body.Value)
	}
	match.Value = *body.Value
	s.deps.Hub.Publish(controls.Change{Type: "plugin-param", Data: map[string]any{
		"id": match.ID, "clap_id": match.ClapID, "name": match.Name, "value": match.Value,
	}})
	writeJSON(w, http.StatusOK, match)
}

func (s *Server) handleDevPluginPage(w http.ResponseWriter, r *http.Request) {
	if !s.devAllowed(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(devPluginHTML))
}

const devPluginHTML = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>polyclav dev plugin panel</title>
<style>
body{font:14px system-ui,sans-serif;margin:24px;background:#111;color:#eee}button,input,select{font:inherit}code,.card{background:#1d1d1d;border:1px solid #333;border-radius:10px;padding:12px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px}.err{color:#ff8a8a}.ok{color:#95ffa8}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #333;padding:6px;text-align:left}input[type=number]{width:9em}</style>
<h1>polyclav CLAP dev panel</h1>
<p>This temporary localhost-only panel is enabled by <code>POLYCLAV_DEV_WEB=1</code>. It is not a plugin editor host.</p>
<div id="msg"></div>
<div class="grid"><section class="card"><h2>Status</h2><pre id="status">loading…</pre></section><section class="card"><h2>Manual poke</h2><p><select id="param"></select> <input id="value" type="number" step="any"> <button id="set">Set</button></p><p id="poke"></p></section></div>
<section class="card"><h2>Available parameters</h2><table><thead><tr><th>ID</th><th>Name</th><th>Value</th><th>Range</th></tr></thead><tbody id="params"></tbody></table></section>
<script>
const $=id=>document.getElementById(id);
let params=[];
async function j(url, opts){const r=await fetch(url,opts); if(!r.ok) throw new Error((await r.json().catch(()=>({error:r.statusText}))).error||r.statusText); return r.json();}
async function refresh(){try{const st=await j('/api/dev/plugin/status'); $('status').textContent=JSON.stringify(st,null,2); params=await j('/api/dev/plugin/params'); renderParams(); $('msg').textContent='';}catch(e){$('msg').className='err'; $('msg').textContent=e.message;}}
function renderParams(){ $('param').innerHTML=params.map(p=>'<option value="'+p.id+'">'+(p.label||p.name||p.id)+' ('+p.id+')</option>').join(''); $('params').innerHTML=params.map(p=>'<tr><td>'+p.id+'</td><td>'+(p.label||p.name)+'</td><td>'+p.value+'</td><td>'+p.min+'…'+p.max+'</td></tr>').join(''); }
$('set').onclick=async()=>{try{const out=await j('/api/dev/plugin/params',{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:$('param').value,value:Number($('value').value)})}); $('poke').className='ok'; $('poke').textContent='applied '+JSON.stringify(out); await refresh();}catch(e){$('poke').className='err'; $('poke').textContent=e.message;}};
refresh();
</script>`
