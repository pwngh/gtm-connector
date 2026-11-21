import React from 'react';

export const Avatar=({name,small=false})=><span className={`avatar ${small?'small':''}`} aria-hidden="true">{String(name ?? '').split(' ').map(x=>x[0]).slice(0,2).join('')}</span>;
export const Pill=({tone='neutral',children,icon:Icon})=><span className={`pill ${tone}`}>{Icon&&<Icon size={13}/>} {children}</span>;
export const Button=({children,primary=false,ghost=false,className='',...props})=><button className={`button ${primary?'primary':''} ${ghost?'ghost':''} ${className}`} {...props}>{children}</button>;
