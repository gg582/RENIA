const token = localStorage.getItem('renia_token');
if (!token) window.location.href = '/login';

let currentSessionId = null;
let currentWorkspaceId = null;
let pendingTool = null;
let pendingChanges = [];
let pendingShell = [];

function logout() {
	localStorage.removeItem('renia_token');
	window.location.href = '/login';
}

function appendMessage(role, content) {
	const div = document.createElement('div');
	div.className = 'message ' + role;
	div.textContent = content;
	document.getElementById('messages').appendChild(div);
	const container = document.getElementById('messages');
	container.scrollTop = container.scrollHeight;
}

function clearMessages() {
	document.getElementById('messages').innerHTML = '';
}

function setApprovalBox(text, tool) {
	const box = document.getElementById('approval-box');
	const txt = document.getElementById('approval-text');
	if (tool) {
		txt.textContent = text || 'Pending action requires approval.';
		box.classList.remove('hidden');
		pendingTool = tool;
	} else {
		box.classList.add('hidden');
		pendingTool = null;
	}
}

function escapeHtml(text) {
	if (!text) return '';
	const div = document.createElement('div');
	div.textContent = text;
	return div.innerHTML;
}

function setChangesBox(reply, changes, shell) {
	const box = document.getElementById('changes-box');
	const preview = document.getElementById('changes-preview');
	const hasChanges = changes && changes.length > 0;
	const hasShell = shell && shell.length > 0;
	if (hasChanges || hasShell) {
		let html = '';
		if (reply) {
			html += '<div class="changes-reply">' + escapeHtml(reply) + '</div>';
		}
		if (hasChanges) {
			html += '<div class="changes-section"><strong>File Changes</strong></div>';
			changes.forEach(ch => {
				html += '<div class="diff-file">' + escapeHtml(ch.path) + ' <span class="diff-action">(' + escapeHtml(ch.action) + ')</span></div>';
				if (ch.action === 'write') {
					const lines = ch.content.split('\n');
					lines.forEach(line => {
						html += '<div class="diff-add">+ ' + escapeHtml(line) + '</div>';
					});
				} else if (ch.action === 'replace') {
					const oldLines = ch.old.split('\n');
					const newLines = ch.new.split('\n');
					oldLines.forEach(line => {
						html += '<div class="diff-del">- ' + escapeHtml(line) + '</div>';
					});
					newLines.forEach(line => {
						html += '<div class="diff-add">+ ' + escapeHtml(line) + '</div>';
					});
				} else if (ch.action === 'diff') {
					const lines = ch.diff.split('\n');
					lines.forEach(line => {
						if (line.startsWith('+') && !line.startsWith('+++')) {
							html += '<div class="diff-add">' + escapeHtml(line) + '</div>';
						} else if (line.startsWith('-') && !line.startsWith('---')) {
							html += '<div class="diff-del">' + escapeHtml(line) + '</div>';
						} else {
							html += '<div class="diff-line">' + escapeHtml(line) + '</div>';
						}
					});
				}
			});
		}
		if (hasShell) {
			html += '<div class="changes-section"><strong>Shell Commands</strong></div>';
			shell.forEach(cmd => {
				html += '<div class="shell-cmd">$ ' + escapeHtml(cmd) + '</div>';
			});
		}
		preview.innerHTML = html;
		box.classList.remove('hidden');
		pendingChanges = changes || [];
		pendingShell = shell || [];
	} else {
		box.classList.add('hidden');
		pendingChanges = [];
		pendingShell = [];
	}
}

async function loadMode() {
	try {
		const res = await fetch('/api/mode', { headers: { 'Authorization': 'Bearer ' + token } });
		if (res.status === 401) { logout(); return; }
		const data = await res.json();
		document.getElementById('mode-select').value = data.mode || 'approval';
	} catch (e) { console.error('loadMode', e); }
}

async function setMode(mode) {
	try {
		const res = await fetch('/api/mode', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify({ mode })
		});
		if (res.status === 401) logout();
	} catch (e) { console.error('setMode', e); }
}

async function loadWorkspaces() {
	try {
		const res = await fetch('/api/workspaces', { headers: { 'Authorization': 'Bearer ' + token } });
		if (res.status === 401) { logout(); return; }
		const ws = await res.json();
		const sel = document.getElementById('workspace-select');
		// keep first option
		while (sel.options.length > 1) sel.remove(1);
		ws.forEach(w => {
			const opt = document.createElement('option');
			opt.value = w.id;
			opt.textContent = w.name;
			sel.appendChild(opt);
		});
	} catch (e) { console.error('loadWorkspaces', e); }
}

function onWorkspaceChange() {
	const sel = document.getElementById('workspace-select');
	currentWorkspaceId = sel.value ? parseInt(sel.value) : null;
}

function showWorkspaceModal() {
	document.getElementById('workspace-modal').classList.remove('hidden');
}

function hideWorkspaceModal() {
	document.getElementById('workspace-modal').classList.add('hidden');
}

async function addWorkspace() {
	const name = document.getElementById('ws-name').value.trim();
	const path = document.getElementById('ws-path').value.trim();
	if (!name || !path) return;
	try {
		const res = await fetch('/api/workspaces', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify({ name, path })
		});
		if (res.status === 401) { logout(); return; }
		hideWorkspaceModal();
		document.getElementById('ws-name').value = '';
		document.getElementById('ws-path').value = '';
		await loadWorkspaces();
	} catch (e) { console.error('addWorkspace', e); }
}

async function loadSessions() {
	try {
		const res = await fetch('/api/sessions', { headers: { 'Authorization': 'Bearer ' + token } });
		if (res.status === 401) { logout(); return; }
		const sessions = await res.json();
		renderSessionList(sessions);
	} catch (err) { console.error('Failed to load sessions', err); }
}

function renderSessionList(sessions) {
	const list = document.getElementById('session-list');
	list.innerHTML = '';
	sessions.forEach(s => {
		const li = document.createElement('li');
		li.className = 'session-item' + (s.id === currentSessionId ? ' active' : '');
		li.textContent = s.title || 'New Chat';
		li.dataset.sessionId = s.id;
		li.onclick = () => selectSession(s.id);
		const del = document.createElement('button');
		del.textContent = '×';
		del.className = 'session-delete';
		del.onclick = (e) => { e.stopPropagation(); deleteSession(s.id); };
		li.appendChild(del);
		list.appendChild(li);
	});
}

async function selectSession(id) {
	currentSessionId = id;
	clearMessages();
	setApprovalBox(null, null);
	setChangesBox(null, null, null);
	try {
		const res = await fetch('/api/sessions/' + id + '/messages', { headers: { 'Authorization': 'Bearer ' + token } });
		if (res.status === 401) { logout(); return; }
		const msgs = await res.json();
		msgs.forEach(m => {
			if (m.role === 'user' || m.role === 'assistant') {
				appendMessage(m.role, m.content);
			}
		});
		// update workspace select to match session
		const sres = await fetch('/api/sessions', { headers: { 'Authorization': 'Bearer ' + token } });
		const sessions = await sres.json();
		const s = sessions.find(x => x.id === id);
		if (s && s.workspace_id) {
			document.getElementById('workspace-select').value = s.workspace_id;
			currentWorkspaceId = s.workspace_id;
		} else {
			document.getElementById('workspace-select').value = '';
			currentWorkspaceId = null;
		}
		document.getElementById('chat-title').textContent = s ? (s.title || 'New Chat') : 'Renia';
		renderSessionList(sessions);
	} catch (err) { console.error('Failed to load messages', err); }
}

async function createNewSession() {
	try {
		const body = { title: 'New Chat' };
		if (currentWorkspaceId) body.workspace_id = currentWorkspaceId;
		const res = await fetch('/api/sessions', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify(body)
		});
		if (res.status === 401) { logout(); return; }
		const data = await res.json();
		currentSessionId = data.id;
		clearMessages();
		setApprovalBox(null, null);
		setChangesBox(null, null, null);
		document.getElementById('chat-title').textContent = 'New Chat';
		await loadSessions();
	} catch (err) { console.error('Failed to create session', err); }
}

async function deleteSession(id) {
	if (!confirm('Delete this chat?')) return;

	// Optimistic UI removal: delete immediately from sidebar
	const item = document.getElementById('session-list').querySelector('li[data-session-id="' + id + '"]');
	if (item) item.remove();

	if (currentSessionId === id) {
		currentSessionId = null;
		clearMessages();
		setApprovalBox(null, null);
		setChangesBox(null, null, null);
		document.getElementById('chat-title').textContent = 'Renia';
	}

	try {
		const res = await fetch('/api/sessions/' + id, {
			method: 'DELETE',
			headers: { 'Authorization': 'Bearer ' + token }
		});
		if (res.status === 401) { logout(); return; }
		await loadSessions();
	} catch (err) {
		console.error('Failed to delete session', err);
		await loadSessions();
	}
}

async function handleChat(e) {
	e.preventDefault();
	if (pendingTool || (pendingChanges.length > 0) || (pendingShell.length > 0)) {
		appendMessage('system', 'Please approve or reject the pending action first.');
		return;
	}
	const input = document.getElementById('prompt');
	const prompt = input.value.trim();
	if (!prompt) return;
	input.value = '';
	appendMessage('user', prompt);
	appendMessage('system', 'Thinking...');

	const body = { prompt };
	if (currentSessionId) body.session_id = currentSessionId;

	try {
		const res = await fetch('/api/chat', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify(body)
		});
		document.getElementById('messages').lastElementChild.remove();
		const data = await res.json();
		if (res.ok) {
			if (data.pending_tool) {
				appendMessage('assistant', data.reply);
				setApprovalBox(data.reply, data.pending_tool);
				if (!currentSessionId && data.session_id) currentSessionId = data.session_id;
			} else if ((data.pending_changes && data.pending_changes.length > 0) || (data.pending_shell && data.pending_shell.length > 0)) {
				appendMessage('assistant', data.reply);
				setChangesBox(data.reply, data.pending_changes, data.pending_shell);
				if (!currentSessionId && data.session_id) currentSessionId = data.session_id;
			} else {
				appendMessage('assistant', data.reply);
				if (!currentSessionId && data.session_id) {
					currentSessionId = data.session_id;
					await loadSessions();
				}
			}
		} else {
			appendMessage('system', 'Error: ' + (data.error || 'Unknown'));
			if (res.status === 401) logout();
		}
	} catch (err) {
		document.getElementById('messages').lastElementChild.remove();
		appendMessage('system', 'Network error');
	}
}

async function approvePending() {
	if (!pendingTool || !currentSessionId) return;
	setApprovalBox(null, null);
	appendMessage('system', 'Executing approved action...');
	try {
		const res = await fetch('/api/approve-tool', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify({ session_id: currentSessionId, tool: pendingTool })
		});
		document.getElementById('messages').lastElementChild.remove();
		const data = await res.json();
		if (res.ok) {
			if (data.pending_tool) {
				appendMessage('assistant', data.reply);
				setApprovalBox(data.reply, data.pending_tool);
			} else {
				appendMessage('assistant', data.reply);
			}
		} else {
			appendMessage('system', 'Error: ' + (data.error || 'Unknown'));
			if (res.status === 401) logout();
		}
	} catch (err) {
		document.getElementById('messages').lastElementChild.remove();
		appendMessage('system', 'Network error');
	}
}

function rejectPending() {
	setApprovalBox(null, null);
	appendMessage('system', 'Action rejected by user.');
}

async function applyPendingChanges() {
	if (!currentSessionId) return;
	setChangesBox(null, null, null);
	appendMessage('system', 'Applying changes...');
	try {
		const res = await fetch('/api/apply-changes', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
			body: JSON.stringify({ session_id: currentSessionId, changes: pendingChanges, shell: pendingShell })
		});
		document.getElementById('messages').lastElementChild.remove();
		const data = await res.json();
		if (res.ok) {
			if (data.pending_tool) {
				appendMessage('assistant', data.reply);
				setApprovalBox(data.reply, data.pending_tool);
			} else if ((data.pending_changes && data.pending_changes.length > 0) || (data.pending_shell && data.pending_shell.length > 0)) {
				appendMessage('assistant', data.reply);
				setChangesBox(data.reply, data.pending_changes, data.pending_shell);
			} else {
				appendMessage('assistant', data.reply);
			}
		} else {
			appendMessage('system', 'Error: ' + (data.error || 'Unknown'));
			if (res.status === 401) logout();
		}
	} catch (err) {
		document.getElementById('messages').lastElementChild.remove();
		appendMessage('system', 'Network error');
	}
}

function rejectPendingChanges() {
	setChangesBox(null, null, null);
	appendMessage('system', 'Changes rejected by user.');
}

// Prompt: Enter sends, Shift+Enter newlines.
document.getElementById('prompt').addEventListener('keydown', (e) => {
	if (e.key === 'Enter' && !e.shiftKey) {
		e.preventDefault();
		handleChat(e);
	}
});

// Initialize.
loadMode();
loadWorkspaces();
loadSessions();
