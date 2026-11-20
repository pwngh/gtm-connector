import React, {useId, useLayoutEffect, useRef, useState} from 'react';
import {motion, useReducedMotion} from 'motion/react';
import {X,AlertCircle} from 'lucide-react';

// One scroll-lock owner lives in App. Dialogs only manage their own focus.
export default function WorkspaceDialog({title, children, onClose, wide=false, drawer=false, returnFocusRef, editing=false, command=false, error, onDismissError}) {
  const ref=useRef(null), errorRef=useRef(null), close=useRef(onClose), titleId=useId();
  const reduce=useReducedMotion();
  const [viewport,setViewport]=useState(null);
  close.current=onClose;
  useLayoutEffect(()=>{
    const vv=window.visualViewport;
    const update=()=>setViewport(vv&&Math.abs(vv.scale-1)<.01?{top:vv.offsetTop,height:vv.height}:null);
    update();vv?.addEventListener('resize',update);vv?.addEventListener('scroll',update);
    return()=>{vv?.removeEventListener('resize',update);vv?.removeEventListener('scroll',update)};
  },[]);
  useLayoutEffect(()=>{
    const previous=returnFocusRef?.current||document.activeElement;
    // Opening a form does not summon a phone keyboard or scroll its page.
    // Search is the intentional exception: its input is the requested action.
    (command?ref.current?.querySelector('input'):ref.current)?.focus({preventScroll:true});
    const handler=event=>{
      if(event.key==='Escape'){event.preventDefault();event.stopPropagation();close.current();return;}
      if(event.key!=='Tab')return;
      const nodes=[...ref.current.querySelectorAll('button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),a[href],summary,[tabindex="0"]')].filter(n=>n.tabIndex>=0&&n.getClientRects().length>0&&!n.closest('[inert]'));
      const first=nodes[0],last=nodes.at(-1),active=document.activeElement;
      if(!first){event.preventDefault();ref.current.focus({preventScroll:true});return;}
      if(!nodes.includes(active)){event.preventDefault();(event.shiftKey?last:first).focus();}
      else if(event.shiftKey&&active===first){event.preventDefault();last.focus();}
      else if(!event.shiftKey&&active===last){event.preventDefault();first.focus();}
    };
    document.addEventListener('keydown',handler);
    return()=>{
      document.removeEventListener('keydown',handler);
      requestAnimationFrame(()=>{
        // A replacement dialog or a newly focused task owns focus now.
        if(document.querySelector('[role="dialog"][aria-modal="true"]'))return;
        const active=document.activeElement;
        if(active&&active!==document.body&&active!==previous&&active.isConnected)return;
        if(previous?.isConnected&&!previous.closest('[inert]')&&previous.getClientRects().length&&getComputedStyle(previous).visibility!=='hidden')previous.focus({preventScroll:true});
        else{const fallback=document.querySelector('main h1')||document.querySelector('main');if(fallback){fallback.tabIndex=-1;fallback.focus({preventScroll:true})}}
      });
    };
  },[]);
  useLayoutEffect(()=>{if(error){errorRef.current?.focus({preventScroll:true});errorRef.current?.scrollIntoView({block:'nearest'})}},[error]);
  return <div className={`dialog-backdrop ${drawer?'drawer-backdrop':''}`} style={viewport?{top:viewport.top,height:viewport.height,bottom:'auto','--dialog-available-height':`${viewport.height}px`}:undefined} onMouseDown={event=>{if(event.target===event.currentTarget)close.current()}}>
    <motion.section initial={reduce?false:{opacity:0,y:drawer?0:16,x:drawer?24:0,scale:drawer?1:.985}} animate={{opacity:1,x:0,y:0,scale:1}} transition={{type:'spring',stiffness:420,damping:36}} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} ref={ref} className={`dialog ${wide?'wide':''} ${drawer?'source-drawer':''} ${editing?'is-editing':''} ${command?'command-dialog':''}`}>
      <div className="dialog-head"><h2 id={titleId}>{title}</h2><button type="button" className="icon-button" aria-label="Close dialog" onClick={()=>close.current()}><X size={21}/></button></div>
      {error&&<div className="dialog-error" role="alert" tabIndex={-1} ref={errorRef}><AlertCircle size={18}/><p>{error}</p><button type="button" className="icon-button" aria-label="Dismiss error" onClick={()=>{onDismissError?.();ref.current?.focus({preventScroll:true})}}><X size={18}/></button></div>}
      {children}
    </motion.section>
  </div>;
}
