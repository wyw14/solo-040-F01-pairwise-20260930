const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");

const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (ch) => (
  { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]
));

let plansById = new Map();
let amendState = null;

function amendmentCell(plan) {
  if (plan.amendable) {
    return '<span class="tag ok">Allowed</span>';
  }
  return `<span class="tag locked" title="${esc(plan.reject_reason)}">Locked</span><div class="muted cell-note">${esc(plan.reject_reason || plan.reject_code)}</div>`;
}

async function refresh() {
  const [plans, chambers, health] = await Promise.all([api("/api/plans"), api("/api/chambers"), api("/healthz")]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";
  plansById = new Map();
  const rows = Object.entries(plans).flatMap(([chamber, items]) => items.map((plan) => {
    plansById.set(plan.id, plan);
    const actions = plan.amendable
      ? `<button type="button" class="btn-inline" data-edit="${plan.id}">Amend</button>`
      : `<button type="button" class="btn-inline" data-view="${plan.id}">View</button>`;
    return `<tr><td>${chamber}</td><td>${shortID(plan.id)}</td><td>v${plan.version}</td><td>${plan.direction}</td><td>${plan.vessels.length}</td><td>${amendmentCell(plan)}</td><td>${new Date(plan.created_at).toLocaleTimeString()}</td><td>${actions}</td></tr>`;
  }));
  document.querySelector("#plans").innerHTML = rows.join("") || '<tr><td colspan="8" class="muted">No active plans</td></tr>';
  const current = chambers.find((item) => item.cycle);
  document.querySelector("#metric-chamber").textContent = current?.chamber_id || "Idle";
  document.querySelector("#metric-stage").textContent = current?.cycle?.stage || "-";
  document.querySelector("#metric-cycle").textContent = shortID(current?.cycle?.id);
}

document.querySelector("#plans").addEventListener("click", async (event) => {
  const editID = event.target?.dataset?.edit;
  const viewID = event.target?.dataset?.view;
  if (editID) {
    await openAmend(editID, true);
  } else if (viewID) {
    await openAmend(viewID, false);
  }
});

async function openAmend(planID, editable) {
  const button = document.querySelector(`[data-edit="${planID}"],[data-view="${planID}"]`);
  await withBusy(button, async () => {
    try {
      const detail = await api(`/api/plans/${planID}`);
      amendState = {
        plan: detail.plan,
        vessels: detail.plan.vessels.map((vessel) => ({ ...vessel })),
        revisions: detail.revisions || [],
        editable: editable && detail.plan.amendable,
      };
      renderAmend();
    } catch (error) { setMessage(message, error.message); }
  });
}

function renderAmend() {
  const panel = document.querySelector("#amend-panel");
  panel.hidden = false;
  const { plan, vessels, revisions, editable } = amendState;
  document.querySelector("#amend-plan-id").textContent = `${shortID(plan.id)} · v${plan.version}`;
  document.querySelector("#amend-meta").textContent =
    `${plan.chamber_id} · ${plan.direction} · ${editable ? "amendment allowed before lock-in" : `locked: ${plan.reject_reason || plan.reject_code}`}`;
  const rows = vessels.map((vessel, index) => `<tr>
    <td>${index + 1}</td><td>${esc(vessel.name)}</td><td>${esc(vessel.call_sign)}</td><td>${vessel.length_m}</td>
    <td>${editable ? `<button type="button" class="btn-inline" data-move="up" data-index="${index}" ${index === 0 ? "disabled" : ""}>Up</button>
        <button type="button" class="btn-inline" data-move="down" data-index="${index}" ${index === vessels.length - 1 ? "disabled" : ""}>Down</button>` : ""}</td>
    <td>${editable && vessels.length > 1 ? `<button type="button" class="btn-inline danger" data-remove="${index}">Remove</button>` : ""}</td>
  </tr>`).join("");
  document.querySelector("#amend-vessels").innerHTML = rows;
  document.querySelector("#amend-add").style.display = editable ? "" : "none";
  document.querySelector("#amend-save").style.display = editable ? "" : "none";
  document.querySelector("#amend-note").disabled = !editable;
  document.querySelector("#amend-revisions").innerHTML = revisions.slice().reverse().map((revision) =>
    `<div class="revision"><strong>v${revision.version}</strong> · ${new Date(revision.changed_at).toLocaleString()}${revision.note ? ` · ${esc(revision.note)}` : ""}
    <span class="muted"> — ${revision.summary.added} added, ${revision.summary.removed} removed, ${revision.summary.moved} reordered</span></div>`
  ).join("");
  setMessage(document.querySelector("#amend-message"), "");
  panel.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

document.querySelector("#amend-vessels").addEventListener("click", (event) => {
  if (!amendState?.editable) return;
  const { move, remove, index } = event.target?.dataset || {};
  const at = Number(index);
  if (move === "up" && at > 0) {
    [amendState.vessels[at - 1], amendState.vessels[at]] = [amendState.vessels[at], amendState.vessels[at - 1]];
    renderAmend();
  } else if (move === "down" && at < amendState.vessels.length - 1) {
    [amendState.vessels[at + 1], amendState.vessels[at]] = [amendState.vessels[at], amendState.vessels[at + 1]];
    renderAmend();
  } else if (remove !== undefined) {
    amendState.vessels.splice(at, 1);
    renderAmend();
  }
});

document.querySelector("#amend-add").addEventListener("click", () => {
  if (!amendState?.editable) return;
  const name = document.querySelector("#add-vessel").value.trim();
  const callSign = document.querySelector("#add-call-sign").value.trim();
  const lengthM = Number(document.querySelector("#add-length").value);
  if (!name || !callSign || lengthM <= 0) {
    setMessage(document.querySelector("#amend-message"), "New vessel needs a name, call sign and positive length.");
    return;
  }
  amendState.vessels.push({ name, call_sign: callSign, length_m: lengthM });
  document.querySelector("#add-vessel").value = "";
  document.querySelector("#add-call-sign").value = "";
  document.querySelector("#add-length").value = "";
  renderAmend();
});

document.querySelector("#amend-cancel").addEventListener("click", () => {
  amendState = null;
  document.querySelector("#amend-panel").hidden = true;
});

document.querySelector("#amend-save").addEventListener("click", async () => {
  if (!amendState?.editable) return;
  const button = document.querySelector("#amend-save");
  await withBusy(button, async () => {
    try {
      const payload = {
        expected_version: amendState.plan.version,
        note: document.querySelector("#amend-note").value.trim(),
        vessels: amendState.vessels.map((vessel) => vessel.id
          ? { id: vessel.id, name: vessel.name, call_sign: vessel.call_sign, length_m: vessel.length_m }
          : { name: vessel.name, call_sign: vessel.call_sign, length_m: vessel.length_m }),
      };
      const result = await api(`/api/plans/${amendState.plan.id}`, { method: "PUT", body: JSON.stringify(payload) });
      setMessage(message, `Plan ${shortID(result.plan.id)} saved as version v${result.plan.version}.`);
      amendState = null;
      document.querySelector("#amend-panel").hidden = true;
      await refresh();
    } catch (error) {
      const reason = error.body?.reason?.message || error.message;
      setMessage(document.querySelector("#amend-message"), `Amendment rejected (v${error.body?.version ?? "?"}): ${reason}`);
      await refresh();
      if (amendState && error.body?.amendable === false) {
        amendState.editable = false;
        amendState.plan = plansById.get(amendState.plan.id) || amendState.plan;
        renderAmend();
      }
    }
  });
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button");
  await withBusy(button, async () => {
    try {
      const result = await api("/api/plans", { method: "POST", body: JSON.stringify({
        chamber_id: document.querySelector("#chamber").value,
        direction: document.querySelector("#direction").value,
        vessels: [{ name: document.querySelector("#vessel").value, call_sign: document.querySelector("#call-sign").value, length_m: Number(document.querySelector("#length").value) }],
      }) });
      setMessage(message, result.queued
        ? `Plan ${shortID(result.plan.id)} queued behind the active transit (chamber busy); amendable until admitted.`
        : `Plan ${shortID(result.plan.id)} admitted to ${result.plan.chamber_id}.`);
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
});

refresh().catch((error) => setMessage(message, error.message));
setInterval(() => refresh().catch(() => {}), 5000);
