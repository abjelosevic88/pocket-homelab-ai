// Shared launcher. Shadow DOM keeps host application styles and layout intact.
if (document.querySelector('pocket-ask-launcher')) return;
const host = document.createElement('pocket-ask-launcher');
const root = host.attachShadow({mode:'open'});
const style = document.createElement('style');
style.textContent = `:host{position:fixed;right:20px;bottom:24px;z-index:2147483000;font:14px system-ui;color:#202821}*{box-sizing:border-box}[hidden]{display:none!important}button{font:inherit;cursor:pointer;border:0;color:inherit;background:none}button:focus-visible{outline:3px solid #72af91;outline-offset:3px}.launch{display:flex;align-items:center;background:#183e31;color:#fff;border:1px solid #ffffff35;border-radius:30px;box-shadow:0 4px 20px #0002}.launch button{min-height:44px;padding:0 17px}.launch .pick{border-left:1px solid #ffffff35;padding:0 13px}svg{vertical-align:middle}.panel{position:fixed;right:18px;top:20px;bottom:20px;width:min(460px,calc(100vw - 28px));background:#fff;border:1px solid #84968a55;border-radius:20px;box-shadow:0 12px 64px #0004;overflow:hidden}.panel[hidden]{display:none}.panel iframe{width:100%;height:100%;border:0}.close{position:absolute;right:12px;top:12px;width:36px;height:36px;border-radius:50%;background:#e9eee9;color:#253d30;font-size:22px}.hint{position:fixed;top:16px;left:50%;transform:translateX(-50%);background:#183e31;color:white;padding:12px 18px;border-radius:12px;box-shadow:0 4px 24px #0003;width:max-content;max-width:90vw}.hint[hidden]{display:none}@media(max-width:650px){:host{right:14px;bottom:calc(80px + env(safe-area-inset-bottom))}.panel{top:auto;bottom:0;right:0;width:100vw;height:min(85dvh,850px);border-radius:20px 20px 0 0}.launch button{min-height:42px}}@media(prefers-reduced-motion:no-preference){.panel{animation:arrive .15s ease-out}@keyframes arrive{from{opacity:.4;transform:translateY(8px)}}}`;
root.append(style);
const launch = document.createElement('div'); launch.className='launch';
launch.innerHTML='<button class="open" type="button" aria-label="Ask AI about this page"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5Z"/></svg> Ask</button><button class="pick" type="button" aria-label="Choose an item to ask about" title="Choose an item to ask about">⌖</button>';
const panel=document.createElement('section');panel.className='panel';panel.hidden=true;panel.setAttribute('role','dialog');panel.setAttribute('aria-label','Pocket Ask');
const close=document.createElement('button');close.className='close';close.type='button';close.textContent='×';close.setAttribute('aria-label','Close Pocket Ask');
const hint=document.createElement('button');hint.className='hint';hint.hidden=true;hint.type='button';hint.textContent='Choose an item on the page · Esc to cancel';
root.append(launch,panel,hint);document.body.append(host);
let frame, source, picking=false, lastFocus;
const coreOrigin=new URL(config.core).origin;
function pageContext(node) {
 const selected=window.getSelection()?.toString().trim()||'';
 const dialog=[...document.querySelectorAll('[role="dialog"], dialog[open], .drawer')].filter(e=>e.getClientRects().length).at(-1);
 const item=node?.closest('tr, [role="row"], article, [data-pocket-ask], [data-id], .card, li')||node||dialog;
 const title=(item?.querySelector('h1,h2,h3,[data-title]')?.textContent||document.querySelector('main h1, h1, .page-title')?.textContent||document.title).trim();
 const link=item?.closest('a[href]')||item?.querySelector('a[href]');
 let itemURL=new URL(location.href);
 if(link){try{const u=new URL(link.href,location.href);if(u.origin===location.origin)itemURL=u}catch{}}
 // Fragments may identify an item. Strip credential-like query parameters.
 for(const key of [...itemURL.searchParams.keys()])if(/token|secret|password|code|key/i.test(key))itemURL.searchParams.delete(key);
 return {app:config.app,url:itemURL.href,title:title.slice(0,300),selection:(selected||(item?.innerText||'')).slice(0,12000)};
}
function sendContext(){if(frame?.contentWindow)frame.contentWindow.postMessage({type:'pocket-ask-context',source,dark:document.documentElement.dataset.theme==='dark'||document.documentElement.classList.contains('dark')},coreOrigin)}
function open(node){
 source=pageContext(node);lastFocus=document.activeElement;panel.hidden=false;launch.hidden=true;
 if(!frame){frame=document.createElement('iframe');frame.title='Ask about your Pocket applications';frame.referrerPolicy='no-referrer';frame.src=config.core+'/ask#'+encodeURIComponent(JSON.stringify({app:config.app,origin:location.origin}));frame.addEventListener('load',sendContext);panel.append(frame,close)}else sendContext();
 close.focus();
}
function hide(){panel.hidden=true;launch.hidden=false;lastFocus?.focus?.()}
function pick(on){picking=on;hint.hidden=!on;launch.hidden=on;document.documentElement.style.cursor=on?'crosshair':''}
launch.querySelector('.open').onclick=()=>open();launch.querySelector('.pick').onclick=()=>pick(true);close.onclick=hide;hint.onclick=()=>pick(false);
document.addEventListener('click',e=>{if(!picking||e.composedPath().includes(host))return;e.preventDefault();e.stopImmediatePropagation();pick(false);open(e.target)},true);
document.addEventListener('keydown',e=>{if(e.key==='Escape'){if(picking)pick(false);else if(!panel.hidden)hide()}});
window.addEventListener('message',e=>{if(e.origin!==coreOrigin||e.source!==frame?.contentWindow)return;if(e.data?.type==='pocket-ask-ready')sendContext();if(e.data?.type==='pocket-ask-close')hide()});
window.addEventListener('pocket-ask',e=>open(e.detail?.element));
// App integrations may dispatch pocket-ask with an item element; the picker also
// works on cards and rows that open drawers without changing the browser URL.

// Hub's active app supplies its own launcher. Preserve one visible entry point,
// and let a panel follow navigation without replacing an existing chat's source.
let lastLocation=location.href, navigationTimer;
function pageChanged(){
 if(config.app==='hub')host.style.display=location.pathname.startsWith('/app/')?'none':'';
 if(location.href===lastLocation)return;
 lastLocation=location.href;clearTimeout(navigationTimer);
 navigationTimer=setTimeout(()=>{if(frame&&!panel.hidden){source=pageContext();sendContext()}},150);
}
new MutationObserver(pageChanged).observe(document.querySelector('#root')||document.body,{childList:true,subtree:true});
window.addEventListener('popstate',pageChanged);pageChanged();
