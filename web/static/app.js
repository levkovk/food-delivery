'use strict';

const STATUS = {
  created: 'Принят',
  paid: 'Оплачен',
  cooking: 'Готовится',
  ready: 'Готов',
  delivering: 'В пути',
  delivered: 'Доставлен',
  declined: 'Отклонён',
  cancelled: 'Отменён',
};
const COLUMNS = ['created', 'paid', 'cooking', 'ready', 'delivering', 'delivered'];
const ZONES = { center: 'Центр', north: 'Север', south: 'Юг' };

const state = { token: sessionStorage.getItem('token'), role: sessionStorage.getItem('role'), user: sessionStorage.getItem('user'), tab: 'board', restaurant: null, stream: null };
const $ = (sel) => document.querySelector(sel);
const rub = (n) => `${Number(n).toLocaleString('ru-RU')} ₽`;
const time = (s) => new Date(s).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') node.className = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c != null) node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

async function api(method, path, body) {
  const res = await fetch(`/api${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(state.token ? { Authorization: `Bearer ${state.token}` } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401 && path !== '/auth/login') {
    logout();
    throw new Error('Сессия истекла');
  }
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new Error((data && data.error) || `Ошибка ${res.status}`);
  return data;
}

function badge(status) {
  const cls = status === 'delivered' || status === 'authorized' ? 'ok'
    : status === 'declined' || status === 'cancelled' ? 'bad'
    : status === 'refunded' ? 'warn' : '';
  return el('span', { class: `badge ${cls}` }, STATUS[status] || status);
}

/* Вход */

function showLogin() {
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#login-username').focus();
}

$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('#login-error').textContent = '';
  try {
    const data = await api('POST', '/auth/login', { username: $('#login-username').value.trim(), password: $('#login-password').value });
    Object.assign(state, { token: data.token, role: data.role, user: data.username });
    sessionStorage.setItem('token', data.token);
    sessionStorage.setItem('role', data.role);
    sessionStorage.setItem('user', data.username);
    start();
  } catch (err) {
    $('#login-error').textContent = err.message === 'invalid username or password' ? 'Неверный логин или пароль' : err.message;
  }
});

function logout() {
  sessionStorage.clear();
  state.token = null;
  if (state.stream) state.stream.close();
  showLogin();
}
$('#logout').addEventListener('click', logout);

/* Навигация */

document.querySelectorAll('#tabs button').forEach((b) => b.addEventListener('click', () => switchTab(b.dataset.tab)));

function switchTab(tab) {
  state.tab = tab;
  document.querySelectorAll('#tabs button').forEach((b) => {
    b.classList.toggle('active', b.dataset.tab === tab);
    if (b.dataset.tab === tab) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
  });
  document.querySelectorAll('[data-view]').forEach((v) => { v.hidden = v.dataset.view !== tab; });
  refresh();
}

/* Сводка и доска заказов */

async function loadStats() {
  const s = await api('GET', '/stats');
  const by = s.by_status || {};
  const active = ['created', 'paid', 'cooking', 'ready', 'delivering'].reduce((a, k) => a + (by[k] || 0), 0);
  const items = [
    [active, 'в работе'],
    [by.delivered || 0, 'доставлено'],
    [(by.declined || 0) + (by.cancelled || 0), 'отклонено и отменено'],
    [s.avg_delivery_seconds ? `${Math.round(s.avg_delivery_seconds)} с` : '-', 'среднее время доставки'],
    [rub(s.revenue || 0), 'выручка по доставленным'],
  ];
  $('#stats').replaceChildren(...items.map(([v, t], i) => el('div', { class: i === 0 ? 'stat lead' : 'stat' }, el('b', {}, v), el('span', {}, t))));
}

async function loadBoard() {
  const orders = await api('GET', '/orders');
  const columns = COLUMNS.map((st) => {
    const list = orders.filter((o) => o.status === st || (st === 'delivered' && (o.status === 'declined' || o.status === 'cancelled')));
    const shown = st === 'delivered' ? list.slice(0, 12) : list;
    return el('div', { class: 'column' },
      el('h3', {}, STATUS[st], el('span', { class: 'num' }, list.length)),
      shown.map(ticket));
  });
  $('#board').replaceChildren(...columns);
}

function ticket(o) {
  return el('button', { class: `ticket ${o.status}`, onclick: () => showOrder(o.id) },
    el('span', { class: 'ticket-no' }, `№ ${o.id}`), el('span', { class: 'sum' }, rub(o.total)), el('br'),
    el('span', {}, o.restaurant_name), el('br'),
    el('span', { class: 'meta' }, o.courier_name ? `${ZONES[o.zone]} · ${o.courier_name}` : ZONES[o.zone]),
    o.status === 'declined' || o.status === 'cancelled' ? [el('br'), badge(o.status)] : null);
}

async function showOrder(id) {
  const { order: o, history } = await api('GET', `/orders/${id}`);
  const canCancel = o.status === 'created' || o.status === 'paid';
  $('#details').replaceChildren(
    el('h2', {}, `Заказ № ${o.id} `, badge(o.status)),
    el('p', {}, `${o.restaurant_name}, ${ZONES[o.zone]}. ${o.address}`),
    el('table', {}, o.items.map((i) => el('tr', {}, el('td', {}, i.name), el('td', { class: 'num' }, `${i.qty} × ${rub(i.price)}`)))),
    el('p', { class: 'total' }, `Итого ${rub(o.total)}`),
    el('ol', { class: 'history' }, history.map((h) => el('li', {}, el('time', {}, time(h.at)), STATUS[h.status] || h.status, h.note ? ` - ${h.note}` : ''))),
    canCancel ? el('button', { class: 'secondary', onclick: async () => { await api('POST', `/orders/${o.id}/cancel`); $('#details-dialog').close(); refresh(); } }, 'Отменить заказ') : null,
  );
  $('#details-dialog').showModal();
}
$('#details-close').addEventListener('click', () => $('#details-dialog').close());

/* Новый заказ */

let menuCache = [];
$('#new-order').addEventListener('click', async () => {
  const rs = (await api('GET', '/restaurants')).filter((r) => r.active);
  $('#order-restaurant').replaceChildren(...rs.map((r) => el('option', { value: r.id }, `${r.name} (${ZONES[r.zone]})`)));
  await loadOrderMenu();
  $('#order-error').textContent = '';
  $('#order-dialog').showModal();
});
$('#order-restaurant').addEventListener('change', loadOrderMenu);
$('#order-cancel').addEventListener('click', () => $('#order-dialog').close());

async function loadOrderMenu() {
  menuCache = (await api('GET', `/restaurants/${$('#order-restaurant').value}/menu`)).filter((m) => m.available);
  $('#order-items').replaceChildren(...menuCache.map((m, i) => el('label', {},
    el('span', {}, m.name), el('span', { class: 'num' }, rub(m.price)),
    el('input', { type: 'number', min: 0, max: 50, value: i === 0 ? 2 : 0, 'data-id': m.id, oninput: orderTotal }))));
  orderTotal();
}

function orderTotal() {
  let sum = 0;
  $('#order-items').querySelectorAll('input').forEach((inp) => {
    const m = menuCache.find((x) => x.id === Number(inp.dataset.id));
    sum += (Number(inp.value) || 0) * m.price;
  });
  $('#order-total').textContent = `Сумма ${rub(sum)}`;
}

$('#order-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const items = [...$('#order-items').querySelectorAll('input')]
    .filter((i) => Number(i.value) > 0)
    .map((i) => ({ item_id: Number(i.dataset.id), qty: Number(i.value) }));
  try {
    await api('POST', '/orders', { restaurant_id: Number($('#order-restaurant').value), items, address: $('#order-address').value });
    $('#order-dialog').close();
    refresh();
  } catch (err) {
    $('#order-error').textContent = err.message;
  }
});

/* Рестораны */

async function loadRestaurants() {
  const rs = await api('GET', '/restaurants');
  if (!state.restaurant && rs.length) state.restaurant = rs[0].id;
  $('#restaurants').replaceChildren(
    el('tr', {}, el('th', {}, 'Ресторан'), el('th', {}, 'Кухня'), el('th', {}, 'Зона'), el('th', { class: 'num' }, 'Готовка, мин'), el('th', {}, 'Статус')),
    ...rs.map((r) => el('tr', {
      class: `clickable ${r.id === state.restaurant ? 'selected' : ''}`,
      tabindex: 0,
      'aria-selected': r.id === state.restaurant ? 'true' : 'false',
      onclick: () => { state.restaurant = r.id; loadRestaurants(); },
      onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); state.restaurant = r.id; loadRestaurants(); } },
    },
      el('td', {}, r.name), el('td', {}, r.cuisine), el('td', {}, ZONES[r.zone]), el('td', { class: 'num' }, r.prep_minutes),
      el('td', {}, r.active ? el('span', { class: 'badge ok' }, 'принимает') : el('span', { class: 'badge' }, 'закрыт')))));
  const current = rs.find((r) => r.id === state.restaurant);
  if (!current) return;
  const menu = await api('GET', `/restaurants/${current.id}/menu`);
  $('#menu').replaceChildren(
    el('h2', {}, current.name),
    el('ul', {}, menu.map((m) => el('li', {},
      el('span', {}, m.name, m.available ? '' : ' (нет в наличии)'),
      el('span', {}, el('span', { class: 'num' }, rub(m.price)), ' ',
        state.role === 'admin' ? el('button', { class: 'small secondary', onclick: () => toggleItem(m) }, m.available ? 'Скрыть' : 'Вернуть') : null)))));
}

async function toggleItem(m) {
  await api('PUT', `/menu/${m.id}`, { name: m.name, price: m.price, available: !m.available });
  loadRestaurants();
}

/* Курьеры */

async function loadCouriers() {
  const cs = await api('GET', '/couriers');
  $('#couriers').replaceChildren(
    el('tr', {}, el('th', {}, 'Курьер'), el('th', {}, 'Зона'), el('th', {}, 'Транспорт'), el('th', {}, 'Состояние'), el('th', { class: 'num' }, 'Доставок'), el('th', {}, '')),
    ...cs.map((c) => el('tr', {},
      el('td', {}, c.name), el('td', {}, ZONES[c.zone]), el('td', {}, c.transport),
      el('td', {}, !c.on_shift ? el('span', { class: 'badge' }, 'не на смене') : c.busy ? el('span', { class: 'badge warn' }, 'везёт заказ') : el('span', { class: 'badge ok' }, 'свободен')),
      el('td', { class: 'num' }, c.deliveries),
      el('td', {}, el('button', { class: 'small secondary', onclick: async () => { await api('PATCH', `/couriers/${c.id}`, { on_shift: !c.on_shift }); loadCouriers(); } }, c.on_shift ? 'Снять со смены' : 'На смену')))));
}

/* Платежи */

async function loadPayments() {
  const ps = await api('GET', '/payments');
  $('#payments').replaceChildren(
    el('tr', {}, el('th', {}, 'Платёж'), el('th', {}, 'Заказ'), el('th', { class: 'num' }, 'Сумма'), el('th', {}, 'Статус'), el('th', {}, 'Причина'), el('th', {}, 'Время')),
    ...ps.map((p) => el('tr', {},
      el('td', { class: 'num' }, p.id), el('td', { class: 'num' }, `№ ${p.order_id}`), el('td', { class: 'num' }, rub(p.amount)),
      el('td', {}, badge(p.status)), el('td', {}, p.reason || ''), el('td', {}, time(p.created_at)))));
}

/* Поток событий */

function addFeed(ev) {
  const li = el('li', {}, el('time', {}, time(ev.at)), el('span', {}, ev.type), el('span', { class: 'src' }, `№ ${ev.order_id}`),
    el('code', {}, `${ev.source} ${JSON.stringify(ev.data || {})}`));
  const feed = $('#feed');
  feed.prepend(li);
  while (feed.children.length > 80) feed.lastChild.remove();
}

async function openStream() {
  const recent = await api('GET', '/events/recent');
  $('#feed').replaceChildren();
  recent.forEach(addFeed);
  if (state.stream) state.stream.close();
  state.stream = new EventSource(`/api/events/stream?access_token=${encodeURIComponent(state.token)}`);
  state.stream.onopen = () => { $('#feed-state').textContent = 'подключено'; };
  state.stream.onerror = () => { $('#feed-state').textContent = 'переподключение'; };
  state.stream.onmessage = (m) => addFeed(JSON.parse(m.data));
  Object.values({ a: 'order.created', b: 'order.cancelled', c: 'payment.authorized', d: 'payment.declined', e: 'kitchen.accepted', f: 'kitchen.ready', g: 'courier.assigned', h: 'delivery.completed', i: 'delivery.waiting' })
    .forEach((t) => state.stream.addEventListener(t, (m) => {
      addFeed(JSON.parse(m.data));
      if (state.tab === 'board') scheduleRefresh();
    }));
}

/* Обновление */

let pending = null;
function scheduleRefresh() {
  if (!pending) pending = setTimeout(() => { pending = null; refresh(); }, 400);
}

async function refresh() {
  try {
    await loadStats();
    if (state.tab === 'board') await loadBoard();
    if (state.tab === 'restaurants') await loadRestaurants();
    if (state.tab === 'couriers') await loadCouriers();
    if (state.tab === 'payments') await loadPayments();
  } catch (err) {
    console.warn(err);
  }
}

function start() {
  $('#login').hidden = true;
  $('#app').hidden = false;
  $('#who').textContent = `${state.user} (${state.role === 'admin' ? 'администратор' : 'оператор'})`;
  document.querySelectorAll('[data-admin]').forEach((b) => { b.hidden = state.role !== 'admin'; });
  switchTab(new URLSearchParams(location.search).get('tab') || 'board');
  openStream().catch(console.warn);
  setInterval(refresh, 5000);
}

if (state.token) start(); else showLogin();
