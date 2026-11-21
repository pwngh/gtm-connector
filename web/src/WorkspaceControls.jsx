import React from 'react';

export const Pill=({children,icon:Icon})=><span className="pill neutral">{Icon&&<Icon size={13}/>} {children}</span>;
