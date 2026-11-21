import React,{useEffect,useId,useLayoutEffect,useRef,useState} from 'react';
import {Search,CornerDownLeft,Command,Compass} from 'lucide-react';

export default function CommandPalette({items,placeholder,inputLabel,empty}){
  const [query,setQuery]=useState(''),[active,setActive]=useState(0);
  const listId=useId(),optionRefs=useRef([]),inputRef=useRef(null);
  // Take focus only if it is still on the dialog itself, never from Close.
  useLayoutEffect(()=>{
    const input=inputRef.current,active=input?.ownerDocument.activeElement;
    if(input&&(!active||active===input.ownerDocument.body||active===input.closest('[role="dialog"]')))input.focus({preventScroll:true});
  },[]);
  const normalized=query.trim().toLowerCase();
  const results=items.filter(item=>(item.label+' '+item.sub+' '+(item.group||'')).toLowerCase().includes(normalized));
  const index=Math.min(active,Math.max(0,results.length-1));
  useEffect(()=>{optionRefs.current[index]?.scrollIntoView({block:'nearest'})},[index,query]);
  function handleKey(e){
    if(e.key==='ArrowDown'||e.key==='ArrowUp'){
      e.preventDefault();if(results.length)setActive((index+(e.key==='ArrowDown'?1:-1)+results.length)%results.length);
    }else if(e.key==='Enter'&&results[index]){e.preventDefault();results[index].onSelect()}
  }
  return <div className="command-palette">
    <div className="command-input"><Search size={22}/><input ref={inputRef} role="combobox" aria-label={inputLabel} aria-controls={listId} aria-expanded="true" aria-autocomplete="list" aria-activedescendant={results.length?`${listId}-${index}`:undefined} placeholder={placeholder} value={query} onChange={e=>{setQuery(e.target.value);setActive(0)}} onKeyDown={handleKey}/><kbd>esc</kbd></div>
    <div className="command-results" id={listId} role="listbox" aria-label="Workspace results">
      {results.map((item,i)=>{const Icon=item.icon||Compass;return <React.Fragment key={item.label}>{(i===0||results[i-1].group!==item.group)&&<div className="command-group-title" role="presentation">{item.group||'Workspace'}</div>}<button ref={node=>optionRefs.current[i]=node} type="button" tabIndex={-1} role="option" id={`${listId}-${i}`} aria-selected={i===index} className={`command-option ${i===index?'selected':''}`} onMouseMove={()=>setActive(i)} onClick={item.onSelect}><span className="command-option-icon"><Icon size={19}/></span><span className="command-option-copy"><strong>{item.label}</strong><small>{item.sub}</small></span><CornerDownLeft size={15} className="command-option-hint"/></button></React.Fragment>})}
      {!results.length&&<div className="command-empty"><Search size={25}/>{empty}</div>}
    </div>
    <div className="command-footer"><span><Command size={14}/> Workspace command</span><span><kbd>↑</kbd><kbd>↓</kbd> navigate <kbd>↵</kbd> open</span></div>
  </div>;
}
