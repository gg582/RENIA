function showTab(tab) {
	document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
	document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
	document.querySelectorAll('.tab').forEach(t => {
		if (t.getAttribute('onclick') && t.getAttribute('onclick').includes("'" + tab + "'")) {
			t.classList.add('active');
		}
	});
	document.getElementById(tab + '-tab').classList.add('active');
}

async function handleLogin(e) {
	e.preventDefault();
	const username = document.getElementById('login-username').value;
	const password = document.getElementById('login-password').value;
	const msg = document.getElementById('auth-message');
	try {
		const res = await fetch('/api/login', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({username, password})
		});
		const data = await res.json();
		if (res.ok && data.token) {
			localStorage.setItem('renia_token', data.token);
			window.location.href = '/chat';
		} else {
			msg.textContent = data.error || 'Login failed';
			msg.style.color = '#f85149';
		}
	} catch (err) {
		msg.textContent = 'Network error';
		msg.style.color = '#f85149';
	}
}

async function handleRegister(e) {
	e.preventDefault();
	const username = document.getElementById('register-username').value;
	const password = document.getElementById('register-password').value;
	const msg = document.getElementById('auth-message');
	try {
		const res = await fetch('/api/register', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({username, password})
		});
		const data = await res.json();
		if (res.ok) {
			msg.textContent = 'Registered! Please login.';
			msg.style.color = '#7ee787';
			showTab('login');
		} else {
			msg.textContent = data.error || 'Registration failed';
			msg.style.color = '#f85149';
		}
	} catch (err) {
		msg.textContent = 'Network error';
		msg.style.color = '#f85149';
	}
}
