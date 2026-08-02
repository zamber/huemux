// Auth page: token entry. If the user already has a valid token, redirect
// straight back to the app — no point showing the auth form.
(function () {
  var token = getAuthToken();
  if (token) {
    // Test the stored token. If it works, go to the app. If not, stay and let
    // the user enter a new one.
    fetch('/api/config', { headers: { 'Authorization': 'Bearer ' + token } }).then(function (r) {
      if (r.ok) {
        var redir = new URLSearchParams(window.location.search).get('redirect') || '/app.html';
        window.location.replace(redir);
      } else {
        // Token is stale — clear it and show the form.
        clearAuthToken();
      }
    }).catch(function () {
      // Server unreachable — show the form anyway.
    });
  }

  var form = document.getElementById('auth-form');
  var input = document.getElementById('auth-token');
  var error = document.getElementById('auth-error');
  var submit = document.getElementById('auth-submit');

  input.focus();

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var t = input.value.trim();
    if (!t) {
      error.textContent = (typeof HueMuxI18n !== 'undefined'
        ? HueMuxI18n.t('auth.errorEmpty')
        : '') || 'Enter a token.';
      return;
    }

    submit.disabled = true;
    submit.textContent = (typeof HueMuxI18n !== 'undefined'
      ? HueMuxI18n.t('auth.checking')
      : '') || 'Checking…';
    error.textContent = '';

    fetch('/api/config', { headers: { 'Authorization': 'Bearer ' + t } }).then(function (r) {
      if (r.ok) {
        setAuthToken(t);
        var redir = new URLSearchParams(window.location.search).get('redirect') || '/app.html';
        window.location.replace(redir);
      } else if (r.status === 401) {
        error.textContent = (typeof HueMuxI18n !== 'undefined'
          ? HueMuxI18n.t('auth.errorBadToken')
          : '') || 'That token was not accepted. Check it and try again.';
        submit.disabled = false;
        submit.textContent = (typeof HueMuxI18n !== 'undefined'
          ? HueMuxI18n.t('auth.submit')
          : '') || 'Authenticate';
        input.select();
      } else {
        error.textContent = 'Server returned ' + r.status + '. Try again.';
        submit.disabled = false;
        submit.textContent = (typeof HueMuxI18n !== 'undefined'
          ? HueMuxI18n.t('auth.submit')
          : '') || 'Authenticate';
      }
    }).catch(function () {
      error.textContent = (typeof HueMuxI18n !== 'undefined'
        ? HueMuxI18n.t('auth.errorNetwork')
        : '') || 'Could not reach the server. Check that HueMux is running.';
      submit.disabled = false;
      submit.textContent = (typeof HueMuxI18n !== 'undefined'
        ? HueMuxI18n.t('auth.submit')
        : '') || 'Authenticate';
    });
  });
})();
